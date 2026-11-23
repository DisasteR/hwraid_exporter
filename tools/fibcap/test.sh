#!/bin/sh
# Construit fibcap.so et le vérifie avec test_fibcap (ioctl sur /dev/null).
set -eu
cd "$(dirname "$0")"
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
gcc -O2 -Wall -Wextra -shared -fPIC -o fibcap.so fibcap.c
gcc -O2 -Wall -o "$out/test_fibcap" test_fibcap.c -lpthread
HWRAID_FIBCAP_OUT="$out/cap.log" LD_PRELOAD="$PWD/fibcap.so" "$out/test_fibcap"
log="$out/cap.log"
fail=0
check() { if ! eval "$2"; then echo "ÉCHEC : $1"; fail=1; else echo "ok : $1"; fi; }
# Requête attendue : octets 0..511, avec Size = 0x01e0 et SenderSize = 0x0200.
exp=$(i=0; while [ $i -lt 512 ]; do case $i in 8) printf e0;; 9) printf 01;; 10) printf 00;; 11) printf 02;; *) printf '%02x' $((i % 256));; esac; i=$((i + 1)); done)
check "SENDFIB journalisé sur 512 octets" \
  "grep -m1 'phase=REQ ioctl=0x42008 len=512 ' '$log' >/dev/null"
check "échec ENOTTY journalisé sans charge (le tampon n'est pas une réponse)" \
  "grep -m1 'phase=RSP ioctl=0x42008 ret=-1 errno=25\$' '$log' >/dev/null && ! grep -q 'phase=RSP.* fib=' '$log'"
check "octets de la requête exacts sur les 512 octets" \
  "[ \"\$(grep -m1 'phase=REQ ioctl=0x42008 len=512 ' '$log' | sed 's/.* fib=//')\" = \"$exp\" ]"
check "SenderSize > Size+32 : 768 octets" \
  "grep -m1 'ioctl=0x42168 len=768 ' '$log' >/dev/null"
check "taille aberrante : taille seule, corps non lu" \
  "grep -m1 'phase=REQ ioctl=0x42008 size=65567\$' '$log' >/dev/null"
check "GET_HBA_INFO : code seul, sans FIB" \
  "grep 'ioctl=0x42198' '$log' | grep -vq 'fib='"
check "ioctl étranger non journalisé" \
  "! grep -q 'ioctl=0x5401' '$log'"
check "200 appels concurrents complets (REQ et RSP)" \
  "[ \$(grep -c 'phase=REQ ioctl=0x42008 len=64 ' '$log') -eq 200 ] && [ \$(grep -c 'phase=REQ ioctl=0x42008 ' '$log') -eq \$(grep -c 'phase=RSP ioctl=0x42008 ' '$log') ]"
check "aucune ligne tronquée ou mélangée" \
  "[ \$(grep -vcE '^seq=[0-9]+ t=[0-9]+\\.[0-9]{9} pid=[0-9]+ tid=[0-9]+ phase=(REQ|RSP) ioctl=0x[0-9a-f]+( ret=-?[0-9]+ errno=[0-9]+)?( len=[0-9]+ fib=[0-9a-f]+| size=[0-9]+| fib=illisible)?\$' '$log') -eq 0 ]"
check "pointeur invalide : ligne illisible, sans plantage" \
  "grep -m1 'phase=REQ ioctl=0x42008 fib=illisible\$' '$log' >/dev/null"
check "horodatage sur 9 chiffres et pid présent" \
  "[ \$(grep -vcE '^seq=[0-9]+ t=[0-9]+\\.[0-9]{9} pid=[0-9]+ tid=' '$log') -eq 0 ]"
check "longueur hexadécimale égale à len" \
  "awk '/ fib=/ { split(\$0, a, \" len=\"); split(a[2], b, \" fib=\"); if (length(b[2]) != 2 * b[1]) bad++ } END { exit bad }' '$log'"
check "chaque seq a exactement un REQ et un RSP" \
  "awk '{ c[\$1 \" \" \$5]++; s[\$1]++ } END { for (k in c) if (c[k] != 1) bad++; for (k in s) if (s[k] != 2) bad++; exit bad }' '$log'"
# Journal refusé : fichier existant, lien symbolique, chemin relatif. Les
# appels sont alors transmis sans capture et le programme réussit.
touch "$out/exists.log"
ln -s "$out/target.log" "$out/link.log"
for p in "$out/exists.log" "$out/link.log" "rel.log"; do
  check "journal refusé ($p), appels transmis" \
    "HWRAID_FIBCAP_OUT='$p' LD_PRELOAD='$PWD/fibcap.so' '$out/test_fibcap' >/dev/null 2>&1 && [ ! -s '$out/exists.log' ] && [ ! -e '$out/target.log' ] && [ ! -e rel.log ]"
done
echo "--- symboles glibc requis par fibcap.so :"
objdump -T fibcap.so | awk '/GLIBC_/ {print $5, $NF}' | sort -u
exit $fail
