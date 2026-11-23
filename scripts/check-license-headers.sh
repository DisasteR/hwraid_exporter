#!/bin/sh
# Copyright 2026 Benjamin Saiz
# SPDX-License-Identifier: Apache-2.0
#
# Vérifie que chaque fichier source suivi par git porte l'en-tête de licence
# SPDX (Go, C, scripts, Dockerfile, YAML, unités systemd).
set -eu
fail=0
for f in $(git ls-files -- '*.go' '*.c' '*.sh' '*.yml' '*.service' 'Dockerfile'); do
    if ! head -5 "$f" | grep -q 'SPDX-License-Identifier: Apache-2.0'; then
        echo "en-tête de licence absent : $f" >&2
        fail=1
    fi
done
exit $fail
