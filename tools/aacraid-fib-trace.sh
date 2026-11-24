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
# un bilan par programme affiché. Un redémarrage retire aussi la sonde.
#
# Usage : sudo tools/aacraid-fib-trace.sh [fichier]
#   Sans argument, la trace va dans un répertoire privé créé par mktemp sous
#   /var/tmp. Un fichier donné ne doit pas exister : il est créé en 0600, sans
#   jamais écraser ni suivre un fichier existant.
set -eu

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

# Sortie créée avant la sonde, de façon exclusive (noclobber : échec si le
# chemin existe, lien compris) et privée ; le descripteur 3 reste ouvert.
umask 077
if [ $# -gt 0 ]; then
	OUT=$1
else
	OUT=$(mktemp -d /var/tmp/aacfib.XXXXXX)/trace.txt
fi
set -C
if ! exec 3>"$OUT"; then
	echo "impossible de créer $OUT (existe déjà ?)" >&2
	exit 1
fi
set +C

summary() {
	awk '{for (i = 1; i <= NF; i++) if ($i ~ /^ret=/) print $1, $i}' "$OUT" |
		sed -E 's/-[0-9]+ ret/ ret/' | sort | uniq -c
}

READER=
probe=0
cleanup() {
	trap - EXIT INT TERM
	failed=0
	if [ -n "$READER" ]; then
		kill "$READER" 2>/dev/null || true
		wait "$READER" 2>/dev/null || true
	fi
	if [ -d "$I" ]; then
		if [ -e "$I/events/aacfib/ret/enable" ]; then
			echo 0 > "$I/events/aacfib/ret/enable" || failed=1
		fi
		rmdir "$I" || failed=1
	fi
	if [ "$probe" -eq 1 ]; then
		echo '-:aacfib/ret' >> "$T/kprobe_events" || failed=1
	fi
	if [ -e "$I" ] || grep -q '^r[0-9]*:aacfib/ret ' "$T/kprobe_events" 2>/dev/null; then
		failed=1
	fi
	if [ "$failed" -ne 0 ]; then
		echo "ÉCHEC du retrait : vérifier $I et $T/kprobe_events" >&2
	else
		echo "sonde retirée"
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

# $retval est lu par le noyau, pas par le shell.
# shellcheck disable=SC2016
echo 'r:aacfib/ret aac_fib_send ret=$retval:s32' >> "$T/kprobe_events"
probe=1
mkdir "$I"
echo 'ret != -115' > "$I/events/aacfib/ret/filter"
echo 1 > "$I/events/aacfib/ret/enable"
echo "sonde active, écriture dans $OUT (Ctrl+C pour arrêter)"
# Lecteur en arrière-plan : le shell attend dans wait, qu'un signal
# interrompt aussitôt.
cat "$I/trace_pipe" >&3 &
READER=$!
wait "$READER"
