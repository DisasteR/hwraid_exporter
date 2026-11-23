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
check "SENDFIB journalisé sur 512 octets" \
  "grep -m1 'phase=REQ ioctl=0x42008 len=512 ' '$log' >/dev/null"
check "échec ENOTTY journalisé sans charge (le tampon n'est pas une réponse)" \
  "grep -m1 'phase=RSP ioctl=0x42008 ret=-25 errno=25\$' '$log' >/dev/null && ! grep -q 'phase=RSP.* fib=' '$log'"
check "octets de la requête exacts (0..511, Size/SenderSize posés)" \
  "grep -m1 'phase=REQ ioctl=0x42008 len=512 ' '$log' | grep -q 'fib=0001020304050607e0010002'"
check "SenderSize > Size+32 : 768 octets" \
  "grep -m1 'ioctl=0x42168 len=768 ' '$log' >/dev/null"
check "taille aberrante plafonnée à 2048" \
  "grep -m1 'ioctl=0x42008 len=2048 ' '$log' >/dev/null"
check "GET_HBA_INFO : code seul, sans FIB" \
  "grep 'ioctl=0x42198' '$log' | grep -vq 'fib='"
check "ioctl étranger non journalisé" \
  "! grep -q 'ioctl=0x5401' '$log'"
check "200 appels concurrents complets (REQ et RSP)" \
  "[ \$(grep -c 'phase=REQ ioctl=0x42008 len=64 ' '$log') -eq 200 ] && [ \$(grep -c 'phase=REQ ioctl=0x42008 ' '$log') -eq \$(grep -c 'phase=RSP ioctl=0x42008 ' '$log') ]"
check "aucune ligne tronquée ou mélangée" \
  "[ \$(grep -vcE '^seq=[0-9]+ t=[0-9]+\\.[0-9]+ tid=[0-9]+ phase=(REQ|RSP) ioctl=0x[0-9a-f]+( ret=-?[0-9]+ errno=[0-9]+)?( len=[0-9]+ fib=[0-9a-f]+)?\$' '$log') -eq 0 ]"
check "longueur hexadécimale égale à len" \
  "awk '/ fib=/ { split(\$0, a, \" len=\"); split(a[2], b, \" fib=\"); if (length(b[2]) != 2 * b[1]) bad++ } END { exit bad }' '$log'"
check "chaque REQ a son RSP (même seq)" \
  "[ \$(cut -d' ' -f1,4 '$log' | sort -u | cut -d' ' -f1 | uniq -c | awk '\$1 != 2' | wc -l) -eq 0 ]"
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
