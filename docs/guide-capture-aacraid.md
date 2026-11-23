# Annexe aacraid : capturer les échanges d'ARCCONF

Cette annexe donne les commandes du [guide de capture](guide-capture.md) pour une carte gérée par le pilote Linux `aacraid` (Adaptec Series 6, 7 et 8). Les règles communes du guide s'appliquent : consultation uniquement, bibliothèque protégée, répertoire réservé à root, transmission par un canal privé.

## 1. Cas propres à aacraid

- La carte n'annonce pas l'interface des lectures d'état ([aacraid-interface.md](aacraid-interface.md), section 7) : `hwraid_collector_supported{collector="volumes"} 0`.
- La carte annonce cette interface, avec un modèle ou un firmware jamais qualifié.

## 2. Prérequis

- La carte, sous Linux x86-64 avec la glibc, et le pilote `aacraid` chargé.
- ARCCONF 2.x, obtenu auprès du constructeur pour cette carte et installé normalement.
- `gcc` (sans droits particuliers) et `sudo` pour lancer ARCCONF.
- L'accord de l'exploitant du serveur.

Commandes de consultation utilisées : `GETVERSION`, `GETCONFIG`, `GETSTATUS` et `GETCONFIGJSON`. Vérifier la syntaxe avec l'aide de la version installée (`arcconf help`) : certaines versions n'ont pas `GETCONFIGJSON`.

## 3. Construire et installer fibcap

La bibliothèque [`fibcap`](../tools/fibcap) journalise les ioctl de gestion `aacraid` qu'ARCCONF envoie par la glibc. Les commandes au firmware (FIB) sont entièrement contenues dans le tampon de l'appel : elle les journalise avant et après l'appel, sur la plage que le pilote copie. Le format du journal est décrit en tête de `fibcap.c`.

Depuis la racine du dépôt, **sans** `sudo` :

```bash
sh tools/fibcap/test.sh && echo FIBCAP_OK
```

Le script compile `tools/fibcap/fibcap.so`, puis le vérifie sans carte. Il doit se terminer par `FIBCAP_OK`. Installer ensuite la bibliothèque dans un répertoire réservé à root :

```bash
sudo install -o root -g root -m 0644 tools/fibcap/fibcap.so /usr/local/lib/fibcap.so
```

## 4. Capturer

Créer, en root, un répertoire de séance réservé à root, et repérer le chemin d'ARCCONF tel que root le voit :

```bash
cap=$(sudo mktemp -d /root/fibcap.XXXXXX) && arc=$(sudo sh -c 'command -v arcconf') && echo "$cap $arc"
```

Relever les versions :

```bash
sudo sh -c '{ uname -r; cat /sys/class/scsi_host/host*/hba_kernel_version /sys/class/scsi_host/host*/model 2>/dev/null; sha256sum "$1"; } > "$2/versions.txt"' sh "$arc" "$cap"
```

Lancer chaque commande avec `fibcap`, en remplaçant `1` par le numéro de la carte dans ARCCONF. Chaque commande a son propre journal ; la ligne affichée donne le code de retour d'ARCCONF et le nombre de commandes firmware réussies et journalisées :

```bash
for c in "GETVERSION" "GETCONFIG 1 AD" "GETCONFIG 1 LD" "GETCONFIG 1 PD" "GETCONFIG 1 AL" "GETSTATUS 1" "GETCONFIGJSON 1"; do n=$(echo "$c" | tr ' ' '-'); sudo sh -c 'HWRAID_FIBCAP_OUT="$1/fibcap-$2.log" LD_PRELOAD=/usr/local/lib/fibcap.so "$3" $4 > "$1/arcconf-$2.out" 2>&1; echo "$2 : code $?, $(grep -c "phase=RSP .* ret=0 .* fib=" "$1/fibcap-$2.log") FIB réussies"' sh "$cap" "$n" "$arc" "$c"; done
```

Un code de retour non nul ou zéro FIB réussie signale une commande indisponible dans cette version d'ARCCONF ou une capture incomplète : le noter dans le ticket.

Copier enfin la séance vers le compte courant. Les fichiers copiés lui appartiennent :

```bash
mkdir -m 700 ~/fibcap-capture && sudo tar -C "$cap" -cf - . | tar -xf - -C ~/fibcap-capture
```

Une fois la transmission terminée, supprimer les deux répertoires : `~/fibcap-capture` et le répertoire de séance sous `/root`.

**ARCCONF dans un conteneur.** Le conteneur doit déjà permettre à ARCCONF d'accéder à la carte (nœud de gestion et `CAP_SYS_RAWIO`). Copier `fibcap.so` dans le conteneur (l'image doit utiliser la glibc), puis lancer la même boucle avec `docker exec` et un répertoire de séance propre au conteneur. La capture ne demande aucun droit supplémentaire.

**États non optimaux.** Refaire la capture dans chaque état utile, en notant l'état et l'heure.

## 5. Transmettre

Suivre la section 4 du [guide](guide-capture.md). Le ticket indique en plus la valeur de `hwraid_collector_supported` et la version d'ARCCONF. Les fichiers utiles sont `fibcap-*.log`, `arcconf-*.out` et `versions.txt`.
