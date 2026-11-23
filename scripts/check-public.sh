#!/bin/sh
# Vérifie que les fichiers suivis par git ne contiennent rien de l'analyse
# interne : ni chemin réservé, ni symbole interne d'ARCCONF.
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
exit $fail
