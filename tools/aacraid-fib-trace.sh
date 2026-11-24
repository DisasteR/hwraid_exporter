#!/bin/sh
# Copyright 2026 Benjamin Saiz
# SPDX-License-Identifier: Apache-2.0
#
# Trace les commandes synchrones envoyées par le pilote aacraid, avec le
# programme qui les envoie, pour repérer celui qui perd des blocs de commande
# (docs/aacraid-defaut-pilote.md). Sonde de retour du noyau sur aac_fib_send,
# posée dans une instance de trace à part : rien à installer, root requis.
#
#   ret=0     commande rendue normalement
#   ret=-512  commande interrompue par un signal (-ERESTARTSYS) : un bloc perdu
#
# La sonde s'exécute à chaque retour de aac_fib_send, entrées-sorties disque
# comprises ; le filtre écarte ensuite celles-ci (-115, -EINPROGRESS).
# Arrêt par Ctrl+C ou SIGTERM : la sonde est retirée, le retrait vérifié, et
# un bilan par programme affiché, avec le nombre de retours que la sonde n'a
# pas pu saisir. Un redémarrage retire aussi la sonde.
#
# Usage : sudo tools/aacraid-fib-trace.sh
#   La trace va dans un répertoire privé créé par mktemp sous /var/tmp, dont
#   le chemin est affiché ; elle n'est lisible que par root.
set -eu

if [ $# -ne 0 ]; then
	echo "usage : $0 (sans argument)" >&2
	exit 2
fi
if [ "$(id -u)" -ne 0 ]; then
	echo "root requis" >&2
	exit 1
fi

T=/sys/kernel/tracing
[ -d "$T/events" ] || T=/sys/kernel/debug/tracing
# Noms propres à cette exécution : une autre sonde n'est jamais touchée.
EV=ret_$$
I=$T/instances/aacfib_$$

# Sortie dans un répertoire neuf, privé, que seul root peut modifier : ni
# fichier existant à écraser, ni chemin remplaçable entre écriture et bilan.
umask 077
DIR=$(mktemp -d /var/tmp/aacfib.XXXXXX)
OUT=$DIR/trace.txt

summary() {
	awk '{for (i = 1; i <= NF; i++) if ($i ~ /^ret=/) print $1, $i}' "$OUT" |
		sed -E 's/-[0-9]+ ret/ ret/' | sort | uniq -c
}

# probe_state : 0 sonde définie, 1 absente, 2 état illisible.
probe_state() {
	if ! [ -r "$T/kprobe_events" ]; then
		return 2
	fi
	if grep -q "^r[0-9]*:aacfib/$EV " "$T/kprobe_events"; then
		return 0
	else
		case $? in
		1) return 1 ;;
		*) return 2 ;;
		esac
	fi
}

READER=
cleanup() {
	trap - EXIT INT TERM
	failed=0
	if [ -n "$READER" ]; then
		kill "$READER" 2>/dev/null || true
		wait "$READER" 2>/dev/null || true
	fi
	# Retours non saisis par la sonde, lus avant son retrait : une trace
	# sans interruption n'en est une que si ce nombre est nul.
	missed=$(awk -v e="$EV" '$1 == e || $1 == "aacfib/" e {print $3}' "$T/kprobe_profile" 2>/dev/null || true)
	if [ -d "$I" ]; then
		if [ -e "$I/events/aacfib/$EV/enable" ]; then
			echo 0 > "$I/events/aacfib/$EV/enable" || failed=1
		fi
		rmdir "$I" || failed=1
	fi
	# La sonde peut être définie même si le script s'est arrêté juste après
	# l'avoir demandée : son état est lu, jamais supposé.
	st=0
	probe_state || st=$?
	if [ "$st" -eq 0 ]; then
		echo "-:aacfib/$EV" >> "$T/kprobe_events" || failed=1
		st=0
		probe_state || st=$?
	fi
	if [ -e "$I" ] || [ "$st" -ne 1 ]; then
		failed=1
	fi
	if [ "$failed" -ne 0 ]; then
		echo "ÉCHEC du retrait : vérifier $I et la sonde aacfib/$EV dans $T/kprobe_events" >&2
	else
		echo "sonde retirée"
	fi
	if [ -n "$missed" ]; then
		echo "retours non saisis par la sonde : $missed"
	else
		echo "retours non saisis par la sonde : inconnu" >&2
	fi
	echo "résultats dans $OUT"
	summary
	return "$failed"
}

# Code de sortie : celui de l'erreur qui a arrêté le script, ou 1 si le
# retrait de la sonde a échoué.
on_exit() {
	rc=$?
	cleanup || rc=1
	exit "$rc"
}
trap on_exit EXIT
trap 'exit 0' INT TERM

exec 3>"$OUT"
# $retval est lu par le noyau, pas par le shell.
echo "r:aacfib/$EV aac_fib_send ret=\$retval:s32" >> "$T/kprobe_events"
mkdir "$I"
echo 'ret != -115' > "$I/events/aacfib/$EV/filter"
echo 1 > "$I/events/aacfib/$EV/enable"
echo "sonde active, écriture dans $OUT (Ctrl+C pour arrêter)"
# Lecteur en arrière-plan : le shell attend dans wait, qu'un signal
# interrompt aussitôt.
cat "$I/trace_pipe" >&3 &
READER=$!
wait "$READER"
