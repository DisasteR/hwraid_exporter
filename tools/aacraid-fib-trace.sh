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
# Arrêt par Ctrl+C ou SIGTERM : la sonde est coupée, le tampon vidé dans la
# sortie, la sonde retirée et le retrait vérifié. Le bilan par programme dit
# si la trace est complète : retours non saisis par la sonde, événements
# perdus par le tampon, limite de taille atteinte. Un redémarrage retire
# aussi la sonde.
#
# Usage : sudo tools/aacraid-fib-trace.sh
#   La trace va dans un répertoire privé créé par mktemp sous /var/tmp, dont
#   le chemin est affiché ; elle n'est lisible que par root. La capture
#   s'arrête à AACFIB_MAX_MB Mio (100 par défaut).
set -eu

if [ $# -ne 0 ]; then
	echo "usage : $0 (sans argument)" >&2
	exit 2
fi
if [ "$(id -u)" -ne 0 ]; then
	echo "root requis" >&2
	exit 1
fi
MAX_MB=${AACFIB_MAX_MB:-100}
case $MAX_MB in
'' | *[!0-9]*)
	echo "AACFIB_MAX_MB doit être un nombre de Mio" >&2
	exit 2
	;;
esac
MAX=$((MAX_MB * 1024 * 1024))

T=/sys/kernel/tracing
[ -d "$T/events" ] || T=/sys/kernel/debug/tracing

# Sortie dans un répertoire neuf, privé, que seul root peut modifier : ni
# fichier existant à écraser, ni chemin remplaçable entre écriture et bilan.
umask 077
DIR=$(mktemp -d /var/tmp/aacfib.XXXXXX)
OUT=$DIR/trace.txt
# Noms propres à cette exécution (PID et partie aléatoire du répertoire) :
# aucune autre sonde, même laissée par une exécution interrompue dont le PID
# aurait été réattribué, n'est jamais touchée.
TAG=$$_${DIR##*.}
EV=ret_$TAG
I=$T/instances/aacfib_$TAG

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

# buffer_lost : événements perdus par le tampon de l'instance (écrasés ou
# rejetés), toutes CPU confondues ; vide si illisible.
buffer_lost() {
	cat "$I"/per_cpu/cpu*/stats 2>/dev/null |
		awk -F: '$1 == "overrun" || $1 == "commit overrun" || $1 == "dropped events" {n += $2; ok = 1}
			END {if (ok) print n}'
}

READER=
LIMIT=0
cleanup() {
	trap - EXIT INT TERM
	failed=0
	# 1. Plus aucun nouvel événement ; 2. le lecteur vide le tampon ;
	# 3. il est arrêté.
	if [ -e "$I/events/aacfib/$EV/enable" ]; then
		echo 0 > "$I/events/aacfib/$EV/enable" || failed=1
	fi
	if [ -n "$READER" ]; then
		sleep 1
		kill "$READER" 2>/dev/null || true
		wait "$READER" 2>/dev/null || true
	fi
	# Pertes, lues avant le retrait : retours que la sonde n'a pas saisis,
	# événements que le tampon n'a pas gardés.
	missed=$(awk -v e="$EV" '$1 == e || $1 == "aacfib/" e {print $3}' "$T/kprobe_profile" 2>/dev/null || true)
	lost=$(buffer_lost || true)
	if [ -d "$I" ]; then
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
	echo "retours non saisis par la sonde : ${missed:-inconnu}"
	echo "événements perdus par le tampon : ${lost:-inconnu}"
	if [ "$LIMIT" -eq 1 ]; then
		echo "limite de $MAX_MB Mio atteinte : capture arrêtée avant Ctrl+C"
	fi
	if [ "${missed:-x}" = 0 ] && [ "${lost:-x}" = 0 ]; then
		echo "trace complète"
	else
		echo "TRACE INCOMPLÈTE : l'absence de ret=-512 ne prouve pas l'absence de perte" >&2
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
echo "sonde active, écriture dans $OUT (Ctrl+C pour arrêter, limite $MAX_MB Mio)"
cat "$I/trace_pipe" >&3 &
READER=$!
# Surveillance de la taille ; un signal est traité au plus une seconde après.
while kill -0 "$READER" 2>/dev/null; do
	sleep 1
	if [ "$(wc -c < "$OUT")" -ge "$MAX" ]; then
		LIMIT=1
		break
	fi
done
