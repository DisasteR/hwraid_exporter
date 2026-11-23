#!/bin/sh
# Vérifie que les fichiers suivis par git ne contiennent rien de l'analyse
# interne : ni chemin réservé, ni symbole interne d'ARCCONF, ni valeur réelle
# de l'hôte de référence.
# Utilisé par la CI ; le hook pre-commit local applique les mêmes motifs.
set -eu
fail=0
paths=$(git ls-files)
for f in $paths; do
    case "$f" in
        preuves-internes/*|*arcconf*|*.reference|*.zip|captures/*)
            echo "chemin interdit : $f" >&2
            fail=1 ;;
    esac
done
pattern='faos_|faux_|CT_GetContainer|FsaGet|getGreenBackup|LocalEnumAdapters|AIF_GetTaskList'
# Ce script et le hook contiennent les motifs eux-mêmes : ils sont exclus.
if git ls-files -z -- . ':!scripts/check-public.sh' | xargs -0 grep -nIE "$pattern" >&2; then
    echo "symbole interne d'ARCCONF présent dans le dépôt" >&2
    fail=1
fi
# Valeurs réelles (numéros de série, WWN, adresses, UID) : liste locale,
# jamais publiée. Les fichiers .hex sont décodés avant la recherche, et les
# formes textuelles cherchées sans tenir compte de la casse. En CI, la liste
# est absente : le contrôle n'a lieu qu'au poste qui détient les captures.
deny=preuves-internes/denylist.txt
if [ -f "$deny" ]; then
    git ls-files -z | python3 -I -c '
import sys
deny = [l.strip() for l in open(sys.argv[1]) if l.strip() and not l.startswith("#")]
bad = 0
for f in sys.stdin.buffer.read().split(b"\0"):
    if not f:
        continue
    raw = open(f, "rb").read()
    hays = [f.lower(), raw.lower()]
    if f.endswith(b".hex"):
        try:
            dec = bytes.fromhex(raw.decode().strip())
        except ValueError:
            print("fichier hexadécimal illisible :", f.decode(), file=sys.stderr)
            bad = 1
            continue
        hays += [dec, dec.lower()]
    for d in deny:
        kind, v = d.split(":", 1)
        needles = [v.lower().encode()] if kind == "text" else [bytes.fromhex(v), v.lower().encode()]
        if any(n in h for n in needles for h in hays):
            print("valeur réelle de l'"'"'hôte de référence dans", f.decode(), file=sys.stderr)
            bad = 1
sys.exit(bad)
' "$deny" || fail=1
fi
exit $fail
