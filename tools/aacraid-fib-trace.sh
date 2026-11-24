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
# Les entrées-sorties disque, asynchrones (-115, -EINPROGRESS), sont filtrées.
# Arrêt par Ctrl+C : la sonde est retirée et un bilan par programme affiché.
# Un redémarrage retire aussi la sonde.
#
# Usage : sudo tools/aacraid-fib-trace.sh [fichier]   (défaut /tmp/fib-trace.txt)
set -eu

OUT=${1:-/tmp/fib-trace.txt}
T=/sys/kernel/tracing
[ -d "$T/events" ] || T=/sys/kernel/debug/tracing
I=$T/instances/aacfib

if [ "$(id -u)" -ne 0 ]; then
	echo "root requis" >&2
	exit 1
fi
if [ -e "$I" ]; then
	echo "une sonde aacfib est déjà en place ($I)" >&2
	exit 1
fi

summary() {
	awk '{for (i = 1; i <= NF; i++) if ($i ~ /^ret=/) print $1, $i}' "$OUT" |
		sed -E 's/-[0-9]+ ret/ ret/' | sort | uniq -c
}

cleanup() {
	echo 0 > "$I/events/aacfib/ret/enable" 2>/dev/null || true
	rmdir "$I" 2>/dev/null || true
	echo '-:aacfib/ret' >> "$T/kprobe_events" 2>/dev/null || true
	echo "sonde retirée ; résultats dans $OUT"
	summary
}

# $retval est lu par le noyau, pas par le shell.
# shellcheck disable=SC2016
echo 'r:aacfib/ret aac_fib_send ret=$retval:s32' >> "$T/kprobe_events"
trap cleanup EXIT
trap 'exit 0' INT TERM
mkdir "$I"
echo 'ret != -115' > "$I/events/aacfib/ret/filter"
echo 1 > "$I/events/aacfib/ret/enable"
: > "$OUT"
chmod 644 "$OUT"
echo "sonde active, écriture dans $OUT (Ctrl+C pour arrêter)"
cat "$I/trace_pipe" >> "$OUT"
