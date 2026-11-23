// Package nodes crée et nettoie les nœuds de gestion dédiés à l'exporter
// (section 6.2 du cahier des charges). Il ne s'exécute que pendant la phase
// de préparation, en root, avant l'abandon des privilèges.
package nodes

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/DisasteR/hwraid_exporter/internal/sysfs"
)

// Spec décrit un nœud caractère attendu.
type Spec struct {
	Name         string
	Major, Minor uint32
}

// Dev renvoie le numéro de périphérique au format du noyau.
func (s Spec) Dev() uint64 { return unix.Mkdev(s.Major, s.Minor) }

// schema reconnaît les seuls noms que l'exporter crée. Toute autre entrée du
// répertoire est refusée sans modification.
var schema = regexp.MustCompile(`^(aac[0-9]{1,3}|megaraid_sas_ioctl|(hpsa|smartpqi)-[0-9a-f]{4}_[0-9a-f]{2}_[0-9a-f]{2}\.[0-7])$`)

const tmpPrefix = ".hwraid-tmp-"

// CharMajors lit la section « Character devices » de /proc/devices.
func CharMajors(r io.Reader) (map[string]uint32, error) {
	out := map[string]uint32{}
	sc := bufio.NewScanner(r)
	inChar := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "Character devices:":
			inChar = true
			continue
		case line == "Block devices:":
			inChar = false
			continue
		case !inChar || line == "":
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("ligne inattendue dans /proc/devices : %q", line)
		}
		n, err := strconv.ParseUint(f[0], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("major invalide %q : %w", f[0], err)
		}
		out[f[1]] = uint32(n)
	}
	return out, sc.Err()
}

// Desired calcule les nœuds attendus pour les hôtes pris en charge.
// Seul aacraid est géré dans le prototype : major « aac », minor = unique_id.
func Desired(hosts []sysfs.SCSIHost, majors map[string]uint32) ([]Spec, []error) {
	var specs []Spec
	var errs []error
	for _, h := range hosts {
		if h.ProcName != "aacraid" {
			continue
		}
		major, ok := majors["aac"]
		if !ok {
			errs = append(errs, fmt.Errorf("%s : major « aac » absent de /proc/devices", h.Name))
			continue
		}
		minor, err := strconv.ParseUint(h.UniqueID, 10, 8)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s : unique_id %q invalide", h.Name, h.UniqueID))
			continue
		}
		specs = append(specs, Spec{Name: "aac" + h.UniqueID, Major: major, Minor: uint32(minor)})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, errs
}

// Report liste les actions réalisées, pour la journalisation.
type Report struct {
	Created, Replaced, Fixed, Removed []string
}

// ErrForeignEntry : une entrée étrangère au schéma a été trouvée ; rien n'a été modifié.
var ErrForeignEntry = errors.New("entrée étrangère dans le répertoire des nœuds")

// driverMajor associe un nom de nœud au nom de pilote dont le major doit
// être lu dans /proc/devices.
func driverMajor(name string) string {
	switch {
	case strings.HasPrefix(name, "aac"):
		return "aac"
	case name == "megaraid_sas_ioctl":
		return "megaraid_sas_ioctl"
	default: // hpsa-*, smartpqi-* : nœud sg du contrôleur
		return "sg"
	}
}

// Sync met le répertoire dir en conformité avec specs. Les nœuds sont créés
// en 0600 pour uid:gid ; le répertoire appartient à root:gid en 0750.
// majors est la table des majors caractère lue dans /proc/devices : un
// nœud dont le major ne correspond pas à celui de son pilote est refusé.
func Sync(dir string, specs []Spec, majors map[string]uint32, uid, gid int) (Report, error) {
	var rep Report
	want := map[string]Spec{}
	for _, s := range specs {
		if !schema.MatchString(s.Name) {
			return rep, fmt.Errorf("nom de nœud hors schéma : %q", s.Name)
		}
		if m, ok := majors[driverMajor(s.Name)]; !ok || m == 0 || s.Major != m {
			return rep, fmt.Errorf("major %d refusé pour %s (pilote %s : %d)", s.Major, s.Name, driverMajor(s.Name), m)
		}
		if _, dup := want[s.Name]; dup {
			return rep, fmt.Errorf("nœud en double : %q", s.Name)
		}
		want[s.Name] = s
	}

	dfd, err := openDir(dir)
	if err != nil {
		return rep, err
	}
	defer unix.Close(dfd)
	if err := unix.Flock(dfd, unix.LOCK_EX); err != nil {
		return rep, fmt.Errorf("verrou : %w", err)
	}

	// 1. Inventaire et validation complète avant toute modification.
	existing, err := inventory(dfd)
	if err != nil {
		return rep, err
	}
	if err := fixDir(dfd, dir, gid); err != nil {
		return rep, err
	}

	// 2. Créations, remplacements, corrections.
	for _, name := range sortedKeys(want) {
		s := want[name]
		st, ok := existing[name]
		switch {
		case ok && st.Rdev == s.Dev():
			if st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Mode&0o7777 != 0o600 {
				// Jamais de chmod en place : recréation puis renommage atomique.
				if err := create(dfd, s, uid, gid); err != nil {
					return rep, err
				}
				rep.Fixed = append(rep.Fixed, name)
			}
		default:
			if err := create(dfd, s, uid, gid); err != nil {
				return rep, err
			}
			if ok {
				rep.Replaced = append(rep.Replaced, name)
			} else {
				rep.Created = append(rep.Created, name)
			}
		}
	}
	// 3. Suppression des nœuds obsolètes et des temporaires abandonnés.
	for _, name := range sortedKeys(existing) {
		if _, keep := want[name]; keep {
			continue
		}
		if err := unix.Unlinkat(dfd, name, 0); err != nil {
			return rep, fmt.Errorf("suppression de %s : %w", name, err)
		}
		rep.Removed = append(rep.Removed, name)
	}
	return rep, nil
}

// openDir crée le répertoire s'il manque et refuse un lien symbolique ou un
// autre type de fichier. Il ne modifie pas un répertoire existant.
func openDir(dir string) (int, error) {
	var st unix.Stat_t
	err := unix.Lstat(dir, &st)
	switch {
	case errors.Is(err, unix.ENOENT):
		if err := unix.Mkdir(dir, 0o750); err != nil {
			return -1, fmt.Errorf("création de %s : %w", dir, err)
		}
	case err != nil:
		return -1, fmt.Errorf("%s : %w", dir, err)
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return -1, fmt.Errorf("%s n'est pas un répertoire (lien symbolique ou fichier) : refus", dir)
	}
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("ouverture de %s : %w", dir, err)
	}
	// Un répertoire modifiable par un autre que le processus ouvrirait des
	// courses entre validation et action : refus, sans rien modifier.
	if err := unix.Fstat(dfd, &st); err != nil {
		unix.Close(dfd)
		return -1, fmt.Errorf("fstat de %s : %w", dir, err)
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0o022 != 0 {
		unix.Close(dfd)
		return -1, fmt.Errorf("%s appartient à l'UID %d avec le mode %o : refus (propriétaire attendu %d, sans écriture pour le groupe ni les autres)", dir, st.Uid, st.Mode&0o7777, os.Geteuid())
	}
	return dfd, nil
}

// fixDir impose root:gid et le mode 0750 au répertoire, après validation.
func fixDir(dfd int, dir string, gid int) error {
	if err := unix.Fchown(dfd, 0, gid); err != nil {
		return fmt.Errorf("propriétaire de %s : %w", dir, err)
	}
	if err := unix.Fchmod(dfd, 0o750); err != nil {
		return fmt.Errorf("mode de %s : %w", dir, err)
	}
	return nil
}

// inventory renvoie les nœuds caractère du schéma (et les temporaires).
// Toute autre entrée provoque ErrForeignEntry, avant toute modification.
func inventory(dfd int) (map[string]unix.Stat_t, error) {
	f := os.NewFile(uintptr(dupFD(dfd)), "nodes-dir")
	if f == nil {
		return nil, errors.New("duplication du descripteur impossible")
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("lecture du répertoire : %w", err)
	}
	out := map[string]unix.Stat_t{}
	var foreign []string
	for _, name := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, fmt.Errorf("%s : %w", name, err)
		}
		isChar := st.Mode&unix.S_IFMT == unix.S_IFCHR
		if isChar && (schema.MatchString(name) || (strings.HasPrefix(name, tmpPrefix) && schema.MatchString(strings.TrimPrefix(name, tmpPrefix)))) {
			out[name] = st
			continue
		}
		foreign = append(foreign, name)
	}
	if len(foreign) > 0 {
		sort.Strings(foreign)
		return nil, fmt.Errorf("%w : %s", ErrForeignEntry, strings.Join(foreign, ", "))
	}
	return out, nil
}

func create(dfd int, s Spec, uid, gid int) error {
	tmp := tmpPrefix + s.Name
	_ = unix.Unlinkat(dfd, tmp, 0) // temporaire d'une exécution interrompue
	if err := unix.Mknodat(dfd, tmp, unix.S_IFCHR|0o600, int(s.Dev())); err != nil {
		return fmt.Errorf("mknod %s : %w", s.Name, err)
	}
	if err := fix(dfd, tmp, uid, gid); err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	if err := unix.Renameat(dfd, tmp, dfd, s.Name); err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return fmt.Errorf("renommage de %s : %w", s.Name, err)
	}
	return nil
}

// fix fixe le mode puis le propriétaire du temporaire que create vient de
// créer. L'ordre compte : tant que root est propriétaire, il peut changer le
// mode sans CAP_FOWNER, que le profil du conteneur ne donne pas. Le répertoire
// n'étant modifiable que par le processus, aucun lien n'a pu être substitué.
func fix(dfd int, name string, uid, gid int) error {
	if err := unix.Fchmodat(dfd, name, 0o600, 0); err != nil {
		return fmt.Errorf("mode de %s : %w", name, err)
	}
	if err := unix.Fchownat(dfd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("propriétaire de %s : %w", name, err)
	}
	return nil
}

func dupFD(fd int) int {
	n, err := unix.Dup(fd)
	if err != nil {
		return -1
	}
	return n
}

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}
