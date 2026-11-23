# Déploiement et configuration

Deux modes : un conteneur Docker (recommandé, aucun composant sur l'hôte) ou une installation native gérée par systemd. Dans les deux cas, le processus démarre en root le temps de créer ses nœuds de gestion, puis abandonne lui-même ses privilèges : UID et GID 65534, seule capacité conservée celle qu'exige le pilote (`CAP_SYS_RAWIO` pour `aacraid`), `no_new_privs`.

Matériel pris en charge et qualifié : [metrics.md](metrics.md) et la section 8 de [aacraid-interface.md](aacraid-interface.md). Une carte non qualifiée est inventoriée, sans lecture d'état.

## Options

La configuration se fait uniquement par la ligne de commande ; il n'y a ni fichier de configuration ni variable d'environnement.

| Commande | Rôle |
|---|---|
| `hwraid-exporter serve [options]` | collecte et service HTTP |
| `hwraid-exporter discover [-sysfs /sys]` | liste des contrôleurs trouvés, sans root ni commande à la carte |
| `hwraid-exporter version` | version du binaire |

| Option de `serve` | Défaut | Rôle |
|---|---|---|
| `-listen` | `127.0.0.1:9799` | adresse d'écoute HTTP ; `0.0.0.0:9799` dans un conteneur |
| `-interval` | `1m` | intervalle de collecte : rythme des commandes envoyées aux cartes, indépendant du rythme de collecte de Prometheus |
| `-collect-timeout` | le plus petit de `30s` et de `-interval` | délai maximal d'attente d'une carte par cycle ; au plus égal à l'intervalle |
| `-uid`, `-gid` | `65534` | identité après l'abandon des privilèges ; root refusé |
| `-sysfs` | `/sys` | racine de sysfs (tests) |

**Choisir l'intervalle.** Une minute convient : un cycle sur la carte de référence représente une centaine de commandes de lecture. L'intervalle est publié (`hwraid_collection_interval_seconds`) et les règles d'alerte s'y adaptent ; au-delà de 5 minutes, les fenêtres des règles d'état sont à allonger ([alerts.md](alerts.md)). Un intervalle plus court n'apporte rien : Prometheus lit le dernier instantané, une lecture de `/metrics` n'envoie aucune commande.

## Docker

Image : `ghcr.io/disaster/hwraid_exporter:<version>` (par exemple `v0.1.0`). Seuls des tags de version sont publiés, sans `latest` : changer de version est un changement explicite du fichier Compose, et revenir en arrière consiste à remettre le tag précédent.

```yaml
services:
  hwraid-exporter:
    image: ghcr.io/disaster/hwraid_exporter:v0.1.0
    read_only: true
    cap_drop: [ALL]
    cap_add: [MKNOD, CHOWN, SETUID, SETGID, SETPCAP, SYS_RAWIO]
    device_cgroup_rules:
      - "c *:* r"
    security_opt:
      - no-new-privileges:true
    restart: unless-stopped
    stop_grace_period: 30s
    # L'image écoute sur 0.0.0.0:9799 dans le conteneur. Rattacher le
    # conteneur au réseau de Prometheus, ou publier le port en le limitant.
    networks: [monitoring]
    # Autres options : ajouter après la commande, par exemple
    # command: ["serve", "-listen", "0.0.0.0:9799", "-interval", "2m"]

networks:
  monitoring:
    external: true
```

Chaque ligne du profil est justifiée à la section 6.5 du [cahier des charges](../cdc/cahier-des-charges-aacraid-exporter.md) : les capacités ne servent qu'à la préparation, la règle de périphériques autorise la lecture des nœuds créés dans le `/dev` du conteneur. Ni `userns-remap` ni Docker rootless : le pilote refuserait les commandes (`EPERM`).

**Dépôt privé.** Tant que le dépôt est privé, l'image l'est aussi : se connecter au registre sur l'hôte avec un jeton GitHub limité à `read:packages`.

```bash
docker login ghcr.io -u <compte-github>
```

## Installation native (systemd)

Construire le binaire (statique, sans cgo) et l'installer :

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(git describe --tags --always)" -o hwraid-exporter ./cmd/hwraid-exporter
```

```bash
sudo install -m 0755 hwraid-exporter /usr/local/bin/hwraid-exporter
```

```bash
sudo install -m 0644 deploy/systemd/hwraid-exporter.service /etc/systemd/system/
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now hwraid-exporter
```

L'unité [`deploy/systemd/hwraid-exporter.service`](../deploy/systemd/hwraid-exporter.service) borne la phase privilégiée : capacités limitées à celles de la préparation, création et lecture des seuls nœuds `aac` (`DeviceAllow=char-aac rm`), système de fichiers en lecture seule, filtre d'appels système. Elle est démarrée en CI à chaque modification. Pour changer d'option, surcharger `ExecStart` (`systemctl edit hwraid-exporter`) :

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/hwraid-exporter serve -listen 0.0.0.0:9799 -interval 2m
```

Le code de sortie 3 signale que les nœuds attendus ont changé (carte ajoutée ou retirée, pilote rechargé) : `Restart=on-failure` relance le service, qui refait la préparation.

## Prometheus

Collecte :

```yaml
scrape_configs:
  - job_name: hwraid
    scrape_interval: 1m
    static_configs:
      - targets: ['hwraid-exporter:9799']
```

Règles d'alerte d'exemple, à copier à côté de la configuration de Prometheus ([alerts.md](alerts.md)) :

```yaml
rule_files:
  - hwraid-exporter.rules.yml
```

## Vérifier le déploiement

```bash
curl -s http://127.0.0.1:9799/metrics | grep -E '^hwraid_(exporter_build_info|snapshot_ready|discovery_success|management_node_valid|collection_success)'
```

Attendu : `hwraid_snapshot_ready 1`, `hwraid_discovery_success 1`, `hwraid_management_node_valid 1` et chaque `hwraid_collection_success` à 1. Le journal du démarrage indique les nœuds préparés (`nodes prepared`), l'identité et les capacités conservées (`privileges dropped`), puis l'adresse d'écoute et la version (`listening`). Les messages du journal et les textes d'aide des métriques sont en anglais.

| Symptôme | Cause probable |
|---|---|
| `hwraid_management_node_valid 0` | nœud non préparé ou carte changée : voir le journal du démarrage ; en conteneur, règle `device_cgroup_rules` absente |
| `collection_success 0` avec une erreur `EPERM` au journal | `userns-remap`, Docker rootless ou capacité `SYS_RAWIO` retirée |
| `hwraid_collector_supported 0` sur une carte | modèle ou firmware non qualifié : carte inventoriée seulement |
| `hwraid_discovery_success 0` | sysfs partiellement illisible : voir l'avertissement `unreadable SCSI hosts` au journal |
| le conteneur s'arrête avec le code 3 | nœuds attendus changés depuis le démarrage : `restart: unless-stopped` le relance |
