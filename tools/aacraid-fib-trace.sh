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
#             sur les cartes dont les réponses passent par aac_intr_normal
#             (série 6, noyau 4.6 et plus ; docs/aacraid-defaut-pilote.md)
#
# La sonde s'exécute à chaque retour de aac_fib_send, entrées-sorties disque
# comprises ; le filtre écarte ensuite celles-ci (-115, -EINPROGRESS).
# Arrêt par Ctrl+C, SIGTERM ou SIGHUP (fin de la session) : la sonde est coupée, le tampon vidé dans la
# sortie, la sonde retirée et le retrait vérifié. Le bilan ne dit « trace
# complète » que si rien n'a été perdu en route : aucun retour manqué par la
# sonde, aucun événement perdu ni restant dans le tampon, compteurs de
# toutes les CPU lus, autant de lignes écrites que d'événements lus, lecteur
# sans erreur, limite de taille non atteinte, capture démarrée et retrait
# vérifié. Le bilan
# ne couvre que les retours survenus pendant que la sonde était active,
# d'appels entrés dans
# aac_fib_send après son activation (une commande déjà en cours au lancement
# n'est pas vue). Un redémarrage retire aussi
# la sonde. Si le script a disparu sans nettoyer (SIGKILL), retirer à la main
# l'instance et la sonde affichées au démarrage (« sonde … active, instance
# … »), sous la racine de trace qu'il a utilisée (<racine> :
# /sys/kernel/tracing, ou /sys/kernel/debug/tracing si
# /sys/kernel/tracing/events n'existe pas). Le lecteur (cat) peut avoir
# survécu au script et garder la trace ouverte, ce qui fait échouer rmdir :
# l'arrêter d'abord. Repérer son PID d'après sa commande exacte (cat et le
# seul chemin de la trace), puis vérifier que sa sortie est bien le fichier
# de cette capture (« écriture dans … » au démarrage). En cas de doute
# (aucune ligne, plusieurs, autre sortie), ne rien tuer : un redémarrage
# retire aussi l'instance et la sonde.
#   ps -eo pid=,args= | awk -v t='<racine>/instances/aacfib_<pid>_<suffixe>/trace_pipe' '$2 == "cat" && $3 == t && NF == 3'
#   readlink /proc/<PID affiché>/fd/1    # doit être /var/tmp/aacfib.<suffixe>/trace.txt
#   kill -KILL <PID affiché>
#   rmdir <racine>/instances/aacfib_<pid>_<suffixe>
#   echo '-:aacfib/ret_<pid>_<suffixe>' >> <racine>/kprobe_events
#
# Usage : sudo tools/aacraid-fib-trace.sh
#   La trace va dans un répertoire privé créé par mktemp sous /var/tmp, dont
#   le chemin est affiché ; elle n'est lisible que par root. La capture
#   s'arrête à AACFIB_MAX_MB Mio (entier de 1 à 100000, 100 par défaut).
set -eu
# Sortie standard fermée (tube sans lecteur) : une écriture échoue au lieu de
# tuer le shell par SIGPIPE, qui sauterait le nettoyage et laisserait la
# sonde active. L'échec arrête le script (set -e), puis le nettoyage, dont
# les écritures peuvent échouer sans l'interrompre (appelé dans un ||).
trap '' PIPE

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
'' | 0* | *[!0-9]* | ???????*)
	echo "AACFIB_MAX_MB doit être un entier de 1 à 100000" >&2
	exit 2
	;;
esac
if [ "$MAX_MB" -lt 1 ] || [ "$MAX_MB" -gt 100000 ]; then
	echo "AACFIB_MAX_MB doit être un entier de 1 à 100000" >&2
	exit 2
fi
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

# buffer_stats : somme, sur toutes les CPU de l'instance, des événements
# perdus (écrasés ou rejetés), restants et lus, sous la forme
# « perdus restants lus ». Rien si une CPU est illisible ou incomplète.
buffer_stats() {
	n=0
	lost=0
	left=0
	nread=0
	for f in "$I"/per_cpu/cpu*/stats; do
		[ -r "$f" ] || return 0
		v=$(awk -F': *' '
			$1 == "overrun" {o = $2; k++}
			$1 == "commit overrun" {c = $2; k++}
			$1 == "dropped events" {d = $2; k++}
			$1 == "entries" {e = $2; k++}
			$1 == "read events" {r = $2; k++}
			END {if (k == 5) print o + c + d, e, r}' "$f") || return 0
		[ -n "$v" ] || return 0
		# Découpage voulu des trois nombres.
		# shellcheck disable=SC2086
		set -- $v
		lost=$((lost + $1))
		left=$((left + $2))
		nread=$((nread + $3))
		n=$((n + 1))
	done
	[ "$n" -gt 0 ] || return 0
	echo "$lost $left $nread"
}

READER=
READER_FAILED=0
# STARTED : sonde activée et lecteur lancé. Sans cela, aucune capture n'a eu
# lieu, quels que soient les compteurs.
STARTED=0

# ours : le lecteur noté est toujours notre processus fils (son PID n'a pas
# été réattribué à un autre programme).
ours() {
	[ -n "$READER" ] && [ "$(awk '{print $4}' "/proc/$READER/stat" 2>/dev/null)" = "$$" ]
}
LIMIT=0
cleanup() {
	trap - EXIT
	# Un second signal n'interrompt pas le nettoyage.
	trap '' HUP INT TERM
	failed=0
	# 1. Plus aucun nouvel événement.
	if [ -e "$I/events/aacfib/$EV/enable" ]; then
		echo 0 > "$I/events/aacfib/$EV/enable" || failed=1
	fi
	# 2. Le lecteur vide le tampon (au plus 10 s), écrit ce qu'il a lu
	#    (cat écrit chaque lecture aussitôt), puis il est arrêté.
	if [ -n "$READER" ]; then
		i=0
		while [ "$i" -lt 10 ] && ours; do
			stats=$(buffer_stats)
			[ -n "$stats" ] && [ "$(echo "$stats" | cut -d' ' -f2)" -eq 0 ] && break
			sleep 1
			i=$((i + 1))
		done
		sleep 1
		killed=0
		if ours; then
			kill -KILL "$READER" 2>/dev/null || true
			killed=1
		fi
		# Statut du lecteur, lu même s'il s'est arrêté seul entre-temps
		# (processus zombie jusqu'à ce wait) : seul l'arrêt envoyé ici
		# (SIGKILL, 137) est attendu ; une limite ou une erreur le reste.
		rrc=0
		wait "$READER" 2>/dev/null || rrc=$?
		READER=
		if [ "$rrc" -eq 153 ] || [ "$(wc -c < "$OUT")" -ge "$MAX" ]; then
			LIMIT=1
		elif [ "$killed" -eq 0 ] || [ "$rrc" -ne 137 ]; then
			READER_FAILED=1
		fi
	fi
	# 3. Bilan des pertes, lu avant le retrait.
	missed=$(awk -v e="$EV" '$1 == e || $1 == "aacfib/" e {print $3}' "$T/kprobe_profile" 2>/dev/null || true)
	stats=$(buffer_stats)
	lines=$(wc -l < "$OUT")
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
	lost=
	left=
	nread=
	if [ -n "$stats" ]; then
		# Découpage voulu des trois nombres.
		# shellcheck disable=SC2086
		set -- $stats
		lost=$1
		left=$2
		nread=$3
	fi
	echo "retours non saisis par la sonde : ${missed:-inconnu}"
	echo "événements perdus par le tampon : ${lost:-inconnu}, restés dans le tampon : ${left:-inconnu}"
	echo "événements lus : ${nread:-inconnu}, lignes écrites : $lines"
	[ "$LIMIT" -eq 0 ] || echo "limite de $MAX_MB Mio atteinte : capture arrêtée avant Ctrl+C"
	[ "$READER_FAILED" -eq 0 ] || echo "le lecteur de la trace s'est arrêté en erreur" >&2
	[ "$STARTED" -eq 1 ] || echo "capture jamais démarrée (arrêt avant l'activation de la sonde ou le lancement du lecteur)" >&2
	if [ "${missed:-x}" = 0 ] && [ "${lost:-x}" = 0 ] && [ "${left:-x}" = 0 ] &&
		[ "${nread:-x}" = "$lines" ] && [ "$LIMIT" -eq 0 ] && [ "$READER_FAILED" -eq 0 ] &&
		[ "$failed" -eq 0 ] && [ "$STARTED" -eq 1 ]; then
		echo "trace complète"
	else
		echo "TRACE INCOMPLÈTE : l'absence de ret=-512 ne prouve pas l'absence de perte" >&2
	fi
	echo "résultats dans $OUT"
	summary
	return "$failed"
}

# Code de sortie : 0 après Ctrl+C, SIGTERM ou la limite de taille ; celui de
# l'erreur qui a arrêté le script (1 si le lecteur a échoué) ; 1 si le retrait
# de la sonde a échoué.
on_exit() {
	rc=$?
	cleanup || rc=1
	[ "$READER_FAILED" -eq 0 ] || rc=1
	exit "$rc"
}
trap on_exit EXIT
trap 'exit 0' HUP INT TERM

exec 3>"$OUT"
# $retval est lu par le noyau, pas par le shell.
echo "r:aacfib/$EV aac_fib_send ret=\$retval:s32" >> "$T/kprobe_events"
mkdir "$I"
echo 'ret != -115' > "$I/events/aacfib/$EV/filter"
echo 1 > "$I/events/aacfib/$EV/enable"
echo "sonde aacfib/$EV active, instance $I"
echo "écriture dans $OUT (Ctrl+C pour arrêter, limite $MAX_MB Mio)"
# Lecteur : cat écrit chaque lecture aussitôt, sans tampon de lignes. La
# limite est imposée par le noyau (taille de fichier, en blocs de
# 512 octets) : au-delà, cat reçoit SIGXFSZ (statut 153). Une erreur
# d'écriture l'arrête avec un autre statut.
# Un arrêt demandé pendant le lancement du lecteur est mémorisé, puis traité
# dès que son PID est noté : sinon, le nettoyage ne connaîtrait pas le
# lecteur.
PENDING=0
trap 'PENDING=1' HUP INT TERM
(
	# Pas de fichier core sur SIGXFSZ (option hors POSIX, gérée par dash).
	# shellcheck disable=SC3045
	ulimit -c 0 2>/dev/null || true
	ulimit -f "$((MAX / 512))"
	# Un signal adressé à tout le groupe (Ctrl+C, SIGTERM au groupe)
	# n'arrête pas le lecteur avant le vidage : seul le nettoyage l'arrête.
	trap '' HUP INT TERM
	exec cat "$I/trace_pipe"
) >&3 &
READER=$!
STARTED=1
trap 'exit 0' HUP INT TERM
[ "$PENDING" -eq 0 ] || exit 0
# Le shell attend dans wait, qu'un signal interrompt aussitôt.
rc=0
wait "$READER" || rc=$?
READER=
# Limite atteinte : SIGXFSZ (153), ou, si ce signal était ignoré au
# lancement, échec d'écriture une fois la taille maximale atteinte. La
# dernière ligne peut alors être tronquée ; le bilan déclare la trace
# incomplète.
if [ "$rc" -eq 153 ] || [ "$(wc -c < "$OUT")" -ge "$MAX" ]; then
	LIMIT=1
else
	# Le lecteur s'est arrêté de lui-même (erreur d'écriture, tampon fermé) :
	# capture en échec, code de sortie 1 après le nettoyage.
	READER_FAILED=1
	exit 1
fi
