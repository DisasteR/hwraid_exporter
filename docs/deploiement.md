# Déploiement et configuration

Deux modes : un conteneur Docker (recommandé, aucun composant sur l'hôte) ou une installation native gérée par systemd. Dans les deux cas, le processus démarre en root le temps de créer ses nœuds de gestion, puis abandonne lui-même ses privilèges : UID et GID 65534, seule capacité conservée celle qu'exige le pilote (`CAP_SYS_RAWIO` pour `aacraid`), `no_new_privs`.

Matériel qualifié et niveaux de lecture : section 8 de [aacraid-interface.md](aacraid-interface.md#modèles-qualifiés). Une carte d'un modèle non qualifié est inventoriée, sans lecture d'état, sauf autorisation explicite (`-allow-unqualified`, ci-dessous).

## Options

La configuration se fait uniquement par la ligne de commande ; il n'y a ni fichier de configuration ni variable d'environnement.

| Commande | Rôle |
|---|---|
| `hwraid-exporter serve [options]` | collecte et service HTTP |
| `hwraid-exporter discover [-sysfs /sys] [-allow-unqualified …]` | cartes telles que `serve` les voit, en JSON, sans root ni commande à la carte : module, identité et statut de lecture (`read`, `read_allowed`, `needs_allow_unqualified`, `no_module`, `error`), selon les autorisations données ; les contrôleurs non RAID (SATA…) n'y figurent pas. Le statut dit si `serve` enverrait des commandes à la carte (verrou de qualification et d'autorisation), pas que la collecte réussira : nœud, major du pilote dans `/proc/devices`, `GET_HBA_INFO` et ouverture de la carte ne sont vérifiés qu'à la collecte |
| `hwraid-exporter oneshot [options]` | une collecte unique, puis arrêt : voir « Collecte unique » |
| `hwraid-exporter version` | version du binaire |

### Collecte unique (`oneshot`)

`oneshot` fait la préparation de `serve` (root, nœuds dédiés, abandon des privilèges), une seule collecte, écrit le résultat sur la sortie standard et s'arrête. Options : `-sysfs`, `-uid`, `-gid`, `-allow-unqualified` comme `serve`, `-collect-timeout` (30 s par défaut) et `-format` :

- `prometheus` (par défaut) : le texte que `/metrics` servirait après ce cycle, sans échantillon de `hwraid_collection_interval_seconds` (ses lignes `HELP` et `TYPE` restent) ;
- `text` : un résumé lisible, pour un exploitant, des principales valeurs normalisées (carte, collecte, température, protection du cache, volumes en arbre avec leurs membres, disques, tâches de fond), identifiants des labels des métriques ; le terme du constructeur (niveau, état d'un disque, réglage du cache) suit entre parenthèses quand il diffère. Il n'a pas toutes les valeurs des métriques (WWN et firmware des disques, phy, dates de dernier succès, compteurs) et arrondit capacités et avancement : pour un usage outillé, prendre `prometheus`.

```bash
sudo ./hwraid-exporter oneshot -format text
```

Code de sortie : 0 si la découverte est complète et si chaque carte prise en charge a un nœud valide et a réussi toutes ses lectures dans le délai ; 1 sinon (le résultat est tout de même écrit), ou, sans rien écrire, si la préparation échoue, si un `serve` tourne, si les nœuds attendus ont changé depuis la préparation, ou si un signal d'arrêt est reçu pendant la collecte ou l'attente des commandes en cours ; 2 pour une option invalide. `-collect-timeout` borne le cycle ; une commande abandonnée à ce délai est ensuite attendue sans limite avant la sortie, comme à l'arrêt de `serve`. Une carte sans module, ou d'un modèle non qualifié sans option, n'est pas un échec : la sortie le montre.

Précautions :
- `oneshot` utilise ses propres nœuds (`/dev/hwraid_exporter_oneshot`), jamais ceux de `serve`, et les laisse en place, dans ce répertoire réservé à root ;
- il refuse de démarrer si un `hwraid-exporter serve` tourne (recherché dans `/proc` au démarrage) : les deux intercaleraient leurs transferts sur la carte ; lire alors `/metrics`. Ne sont pas détectés : un `serve` invisible depuis ce `/proc` (`oneshot` dans un conteneur, `serve` sur l'hôte), un binaire renommé, un processus dont la ligne de commande est illisible, un `serve` démarré après ce contrôle ; ARCCONF et maxView non plus ;
- deux `oneshot` ne collectent jamais en même temps : le second attend, pendant sa préparation, la fin du premier (verrou sur le répertoire de nœuds) ;
- comme `serve`, il capte SIGTERM, SIGINT (Ctrl-C) et SIGHUP : la commande en cours se termine, aucune nouvelle ne part, les commandes engagées sont attendues, puis rien n'est écrit. SIGKILL (`kill -9`, `timeout -k`) et SIGQUIT (Ctrl-\\) ne sont pas captés et peuvent couper une commande : ne pas l'envelopper dans `timeout` ni le tuer (voir « Limite du pilote aacraid ») ;
- ce n'est pas un mode de supervision : chaque appel envoie la séquence complète d'un premier cycle. Pour une surveillance continue, utiliser `serve`.

| Option de `serve` | Défaut | Rôle |
|---|---|---|
| `-listen` | `127.0.0.1:9799` | adresse d'écoute HTTP ; `0.0.0.0:9799` dans un conteneur |
| `-interval` | `1m` | intervalle de collecte : rythme des commandes envoyées aux cartes, indépendant du rythme de collecte de Prometheus |
| `-collect-timeout` | le plus petit de `30s` et de `-interval` | délai maximal d'attente d'une carte par cycle ; au plus égal à l'intervalle |
| `-uid`, `-gid` | `65534` | identité après l'abandon des privilèges ; root refusé |
| `-sysfs` | `/sys` | racine de sysfs (tests) |
| `-allow-unqualified` | aucune | lire aussi les cartes d'un modèle non qualifié, avec la suite de commandes de l'outil du constructeur : toutes celles du module (valeur : `aacraid`), ou une seule (valeur : `serial:<numéro>`, comme dans son label `controller`) ; répétable ; voir « Cartes non qualifiées » |

**Choisir l'intervalle.** Une minute convient : un cycle ordinaire sur la carte de référence (5 volumes, 8 disques) envoie 69 commandes de lecture, dont `IsAdapterPaused` (704) et la liste des tâches de fond de chacun des 5 volumes, plus une lecture d'avancement par volume dont la liste porte une tâche (en cours ou tout juste terminée) ; la révision (701), les capacités (703, 706) et la carte des connecteurs sont gardées en mémoire. Les capacités sont relues au démarrage, quand la carte ou son firmware change, et à chaque cycle si le numéro de série, le modèle ou le firmware est illisible (une carte dont un identifiant PCI est illisible n'est pas collectée du tout). La carte des connecteurs (708) n'est lue que sur une carte lue (modèle qualifié, ou autorisé par `-allow-unqualified`) qui annonce cette lecture ; elle est relue au démarrage, quand la carte ou son firmware change, à chaque cycle si le numéro de série est illisible, quand les disques présents changent, et au cycle qui suit un désaccord entre sa position d'un disque et celle de l'inventaire. Un intervalle plus long réduit d'autant la sollicitation de la carte, au prix d'une détection plus tardive. L'intervalle est publié (`hwraid_collection_interval_seconds`) et les règles d'alerte s'y adaptent ; au-delà de 5 minutes, les fenêtres des règles d'état sont à allonger ([alerts.md](alerts.md)). Un intervalle plus court n'apporte rien : Prometheus lit le dernier instantané, une lecture de `/metrics` n'envoie aucune commande.

## Docker

Image : `ghcr.io/disaster/hwraid_exporter:<version>` (par exemple `v0.1.0`). Seuls des tags de version sont publiés, sans `latest`, et une version publiée n'est jamais remplacée. Chaque publication GitHub indique l'empreinte de l'image (`ghcr.io/disaster/hwraid_exporter@sha256:…`) : l'épingler dans le fichier Compose garantit les mêmes octets à chaque déploiement. Revenir en arrière consiste à remettre la version ou l'empreinte précédente.

```yaml
services:
  hwraid-exporter:
    image: ghcr.io/disaster/hwraid_exporter:v0.3.0
    read_only: true
    cap_drop: [ALL]
    cap_add: [MKNOD, CHOWN, SETUID, SETGID, SETPCAP, SYS_RAWIO]
    device_cgroup_rules:
      - "c *:* r"
    security_opt:
      - no-new-privileges:true
      # Profil seccomp de l'exporter, à côté du fichier Compose (voir plus bas).
      - seccomp=./seccomp.json
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

**Profil seccomp.** La capacité `SYS_RAWIO`, que l'exporter garde pour le pilote, autorise aussi les appels système `iopl` et `ioperm` (accès direct aux ports d'entrée-sortie de la machine) : le profil seccomp par défaut de Docker les permet dans ce cas. Le profil [`deploy/docker/seccomp.json`](../deploy/docker/seccomp.json) est celui de Docker 29.8.0, moins cette seule autorisation ; copier le fichier à côté du fichier Compose. Il est vérifié en CI (`iopl` refusé, exporter fonctionnel) et sur l'hôte de référence avec la carte le 2026-11-23 (collecte complète). Sans lui, l'exporter fonctionne aussi, avec le profil par défaut.

L'image est publique : aucune authentification n'est nécessaire pour la télécharger.

## Installation native (systemd)

Chaque publication GitHub fournit le binaire statique (`hwraid-exporter-<version>-linux-amd64`, mêmes octets que dans l'image), les licences (`hwraid-exporter-<version>-licenses.tar.gz`), l'unité systemd (`hwraid-exporter.service`) et `SHA256SUMS`, qui couvre ces trois fichiers. Avant l'installation, vérifier le binaire de la version choisie et l'unité. La commande compare les noms entiers littéralement (tout ce qui suit la somme et les deux espaces), exige exactement une entrée pour chacun des deux, puis vérifie leurs deux sommes ; elle échoue au premier écart (remplacer `v0.3.0` par la version installée) :

```bash
V=v0.3.0; awk -v a="hwraid-exporter-$V-linux-amd64" -v b=hwraid-exporter.service '{n = substr($0, 67)} length($1) == 64 && substr($0, 65, 2) == "  " && n == a {na++; print} length($1) == 64 && substr($0, 65, 2) == "  " && n == b {nb++; print} END {if (na != 1 || nb != 1) {print "entrée absente ou en double dans SHA256SUMS" > "/dev/stderr"; exit 1}}' SHA256SUMS > SHA256SUMS.install && sha256sum -c --strict SHA256SUMS.install
```

N'installer que si cette commande réussit.

Ou construire le binaire (statique, sans cgo) :

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(git describe --tags --always)" -o hwraid-exporter ./cmd/hwraid-exporter
```

Puis installer le binaire téléchargé :

```bash
sudo install -m 0755 hwraid-exporter-<version>-linux-amd64 /usr/local/bin/hwraid-exporter
```

ou le binaire construit :

```bash
sudo install -m 0755 hwraid-exporter /usr/local/bin/hwraid-exporter
```

L'unité systemd est jointe à chaque publication GitHub (`hwraid-exporter.service`, somme dans `SHA256SUMS`) ; elle est aussi dans les sources du tag (`deploy/systemd/`) :

```bash
sudo install -m 0644 hwraid-exporter.service /etc/systemd/system/
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now hwraid-exporter
```

Pour une mise à jour, `enable --now` ne relance pas un service déjà actif : après avoir remplacé le binaire et l'unité, redémarrer le service (depuis la v0.1.2, arrêt propre qui attend la commande en cours ; depuis la v0.1.1, voir « Mise à jour depuis la v0.1.1 » : c'est l'ancien processus qui s'arrête, sans cette attente), puis vérifier la version servie :

```bash
sudo systemctl daemon-reload && sudo systemctl restart hwraid-exporter
```

```bash
curl -s http://127.0.0.1:9799/metrics | grep '^hwraid_exporter_build_info'
```

L'unité n'a été démarrée qu'en CI, sans carte : la création du nœud `aac` sous `DeviceAllow=char-aac rm` et la collecte n'y sont pas encore recettées avec une 6805. L'unité [`deploy/systemd/hwraid-exporter.service`](../deploy/systemd/hwraid-exporter.service) borne la phase privilégiée : capacités limitées à celles de la préparation, création et lecture des seuls nœuds `aac` (`DeviceAllow=char-aac rm`), système de fichiers en lecture seule, filtre d'appels système. Elle est démarrée en CI à chaque modification. Pour changer d'option, surcharger `ExecStart` (`systemctl edit hwraid-exporter`). L'exemple garde l'écoute locale : `/metrics` publie numéros de série et WWN sans authentification ; pour une collecte distante, écouter sur une adresse d'un réseau d'administration et filtrer l'accès au port :

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/hwraid-exporter serve -listen 127.0.0.1:9799 -interval 2m
```

Le code de sortie 3 signale que les nœuds attendus ont changé (carte ajoutée ou retirée, pilote rechargé) : `Restart=on-failure` relance le service, qui refait la préparation.

## Cartes non qualifiées

Une carte `aacraid` dont le modèle n'est pas qualifié (`hwraid_controller_qualified 0`) ne reçoit aucune commande (seul `GET_HBA_INFO`, traité par le pilote, est lu ; tous ses collecteurs sont non pris en charge) sauf avec l'option `-allow-unqualified` :

- `-allow-unqualified=aacraid` vaut pour toutes les cartes du module, présentes ou futures ;
- `-allow-unqualified=serial:<numéro>` vaut pour la seule carte dont sysfs annonce ce numéro : celui qui suit `serial:` dans son label `controller` (label `serial:EXAMPLE6805A01` : option `-allow-unqualified=serial:EXAMPLE6805A01`). Le label peut rester `serial:` alors que le sélecteur est suspendu (cas ci-dessous) : `hwraid_collector_supported` dit si la carte est lue. Le numéro est relu à chaque cycle et ne désigne la carte que s'il est lu, et annoncé par aucune autre carte d'un inventaire complet ; un hôte illisible, un numéro de série présent mais illisible sur un hôte quelconque, ou une carte dont les hôtes annoncent des numéros différents, qui pourraient masquer un doublon, suspend tous les sélecteurs `serial:` pour le cycle (l'autorisation `aacraid` n'en dépend pas). Sinon, la carte n'est pas lue à ce cycle ; une carte publiée `pci:` ne peut pas être désignée ainsi. Une carte remplacée par un exemplaire qui annonce un autre numéro n'est plus lue sans nouvelle décision. Au démarrage, une valeur `serial:` qui ne désigne aucune carte présente est signalée (`no card of a module that qualifies its cards announces this serial number`), sans empêcher le démarrage ; les journaux ne reproduisent pas le numéro et désignent la valeur par son rang parmi les valeurs `serial:` distinctes (`serial_selector`).

Elle reçoit alors l'ouverture et la suite de commandes de l'outil du constructeur (tous les morceaux de chaque transfert, sans relecture), protection du cache comprise, et seulement les formes que ses capacités annoncent et que l'exporter implémente ([interface aacraid](aacraid-interface.md#niveaux-de-lecture-dune-carte)). Au démarrage, le journal le signale (`cards of an unqualified model will be read`, ou `the card selected by serial number will be read`).

Risques acceptés en activant l'option :
- des commandes que le constructeur ne documente pas, vérifiées sur le seul modèle de référence et face à des réponses construites : un autre modèle peut répondre autrement ; les contrôles rejettent les incohérences qu'ils détectent (lecture en échec), sans publier d'état inventé ;
- une fréquence plus élevée que l'outil du constructeur, qui ne lit la carte que lorsqu'on l'appelle : choisir un intervalle long, par exemple `-interval 5m` ;
- une commande sans réponse n'est pas interrompue (section suivante) : la carte n'est plus collectée jusqu'à la réponse ;
- la concurrence avec l'outil du constructeur : le pilote sérialise chaque commande, pas un transfert ; une lecture dont l'incohérence est détectée est rejetée.

Une carte d'un modèle qualifié avec un autre firmware que ceux validés est lue sans option, de la même façon (suite de l'outil).

## Limite du pilote aacraid : ne pas interrompre une commande

Sur les cartes dont les réponses passent par `aac_intr_normal` (série 6, dont la 6805, noyau 4.6 et plus), le pilote `aacraid` perd un bloc de commande de gestion, jusqu'au redémarrage de la machine, chaque fois qu'un signal interrompt un programme qui attend la carte ; la réserve n'en compte que 8, partagés par l'exporter, ARCCONF et maxView. Les cartes servies par l'ancien chemin (`aac_response_normal`) rendent le bloc ; les règles ci-dessous restent valables pour toutes. Mécanisme, conséquences, protections de l'exporter et détection : [aacraid-defaut-pilote.md](aacraid-defaut-pilote.md). Règles d'exploitation :

- arrêter le conteneur par `docker compose down` (qui applique `stop_grace_period`) ou par `docker stop -t 30 <conteneur>` (un `docker stop` sans délai peut retomber sur les 10 s par défaut de Docker), jamais `docker rm -f` ni `docker kill`. L'exporter attend sans limite la fin d'une commande déjà envoyée, en le journalisant toutes les 10 s : `stop_grace_period` (ou `TimeoutStopSec` pour systemd) est le seul délai, à régler sur le temps maximal que vous acceptez d'attendre une carte qui ne répond plus. 30 s convient : une commande rend la main en quelques millisecondes (le délai par défaut de Docker est de 10 s). À son expiration, la commande en cours est interrompue : un bloc perdu, et, pour une grande commande, un risque sur son tampon. Un délai sans limite éviterait de tuer une commande figée, mais bloquerait l'arrêt de la machine ([décision](aacraid-defaut-pilote.md#protections-de-lexporter)) ;
- ne pas geler le conteneur ou le service (`docker pause`, `systemctl freeze`) : le gel réveille les threads en attente sans passer par un signal, ce qui peut interrompre une commande en cours malgré les protections de l'exporter [Probable, déduit du code du noyau, non essayé]. Arrêter proprement l'exporter avant tout gel ;
- ne pas tuer un autre outil de gestion pendant qu'il travaille : pas de `timeout` court ni de `SIGKILL` autour d'ARCCONF. Dans un conteneur qui lance ARCCONF, le processus principal doit, à réception de `SIGTERM`, attendre la fin de la commande en cours avant de sortir : quand il sort, le noyau tue tous les processus du conteneur, ARCCONF compris, quel que soit `stop_grace_period` (un init comme `--init` n'y change rien) ;
- protection du cache en panne (`HwraidCacheProtectionFailed`) : régler aussitôt le cache d'écriture des volumes en mode « seulement si protégé » (`wbb`), tant qu'ARCCONF fonctionne, **puis vérifier que la carte l'a coupé** sur chaque volume, après le changement : « Write-cache status: Off » dans ARCCONF, relu après le changement ; `hwraid_volume_write_cache_enabled` à 0 le confirme ensuite. Ce relevé confirme que les nouvelles écritures ne sont plus mises en attente ; laisser ensuite un peu de temps à la carte avant de redémarrer. C'est la mesure la plus fiable contre une perte au redémarrage ;
- réserve épuisée (`cannot allocate memory` sur toutes les commandes envoyées à la carte) : seul un redémarrage de la machine la reconstitue de façon éprouvée. La commande d'arrêt qui vide le cache de la carte ne partira pas, et avec le réglage par défaut du pilote (`cache=2`, à lire dans `/sys/module/aacraid/parameters/cache`) aucun autre vidage n'a lieu. Si le cache d'écriture est actif sans protection, aucun moyen ne permet de vérifier qu'il est vide : arrêter les applications qui écrivent, lancer `sync` et laisser du temps à la carte **réduit le risque sans l'éliminer** ([les deux chemins de vidage](aacraid-defaut-pilote.md#conséquence-à-larrêt--cache-non-vidé)).

### Mise à jour depuis la v0.1.1

Lors de la mise à jour, c'est **l'ancien processus** qui reçoit l'arrêt : remplacer le binaire, l'unité ou l'image ne change rien à son comportement. La v0.1.1 ne protège pas ses commandes : à l'arrêt, elle ferme son serveur et sort sans attendre la carte, et elle pouvait déjà perdre des blocs en fonctionnement (signaux internes du runtime Go). Un arrêt qui tombe pendant un de ses cycles de collecte, environ 1 s par intervalle, peut donc coûter un bloc de la réserve.

Pour limiter ce risque :

- arrêter l'ancienne version une seule fois, par `docker stop -t 30 <conteneur>` ou `systemctl stop`, et démarrer directement la nouvelle ;
- pour savoir si cet arrêt a coûté un bloc, lancer la sonde (`tools/aacraid-fib-trace.sh`, section « Détecter une fuite » de [aacraid-defaut-pilote.md](aacraid-defaut-pilote.md)) et la laisser tourner au moins un intervalle de collecte avant l'arrêt : une sonde ne voit que les commandes entrées dans le pilote après son activation. Une ligne `hwraid-exporter ret=-512` signale alors une commande interrompue, donc un bloc perdu sur la série 6 (chemin `aac_intr_normal`). La sonde ne renseigne pas sur les blocs déjà perdus avant son lancement ;
- un bloc perdu ne revient qu'au redémarrage de la machine ; la réserve en compte 8.

Les arrêts suivants, par la v0.1.2, attendent la commande en cours.

### Remplacer une carte ou mettre à jour son firmware

Arrêter l'exporter avant l'intervention et le redémarrer après. À chaque cycle, l'exporter ouvre le nœud et vérifie que `GET_HBA_INFO` désigne bien la carte attendue (numéro, adresse et identifiants PCI) ; si le modèle lu dans sysfs est qualifié ou autorisé par `-allow-unqualified`, il ouvre alors la carte comme l'outil du constructeur (704, puis 701, 703 et, si la 703 l'annonce, 706, ou leurs réponses gardées en mémoire si adresse, nœud, identifiants PCI, numéro de série et firmware lus dans sysfs n'ont pas changé) et n'envoie les lectures suivantes que si cette ouverture réussit ; sinon, il n'envoie aucune commande à la carte. Après une mise à jour vers un firmware absent de la table des modèles qualifiés, la carte reste lue, avec la suite de commandes de l'outil. `GET_HBA_INFO` ne donne ni le firmware ni le numéro de série : une carte changée entre la lecture de sysfs et l'ouverture du nœud, dans le même cycle, avec les mêmes numéro, adresse et identifiants PCI (même modèle, autre firmware ou autre exemplaire), ne serait pas détectée avant l'envoi des commandes.

Une carte n'est ni remplacée ni mise à jour à chaud : un changement de numéro de série, de modèle, de firmware, d'identifiants PCI ou de nœuds à la même adresse PCI pendant que l'exporter tourne signale un rechargement du pilote ou une anomalie. L'exporter le journalise (`card identity changed at the same PCI address while running`, avec l'adresse et les champs changés, sans leurs valeurs), oublie les réponses gardées et l'historique de l'adresse, et relit la carte selon sa nouvelle identité.

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
curl -s http://127.0.0.1:9799/metrics | grep -E '^hwraid_(exporter_build_info|snapshot_ready|discovery_success|management_node_valid|collection_success|collector_supported)'
```

Attendu : `hwraid_snapshot_ready 1`, `hwraid_discovery_success 1`, et, **pour chaque carte attendue** (label `controller`), une ligne `hwraid_management_node_valid … 1` et une ligne `hwraid_collection_success{…,collector="…"} 1` pour chaque collecteur pris en charge (aacraid : jusqu'à `capabilities`, `cache_protection`, `temperature`, `disk_inventory`, `disk_location`, `disks`, `volumes`). Un collecteur que la carte ne permet pas apparaît à la place dans `hwraid_collector_supported{…} 0` : vérifier qu'il est attendu pour ce modèle et ce firmware (sur la 6805 de référence, les sept sont pris en charge). Les seules métriques globales ne suffisent pas : elles sont publiées même sans aucune carte collectée. Le journal du démarrage indique les nœuds préparés (`nodes prepared`), l'identité et les capacités conservées (`privileges dropped`), puis l'adresse d'écoute et la version (`listening`). Les messages du journal et les textes d'aide des métriques sont en anglais.

| Symptôme | Cause probable |
|---|---|
| `hwraid_management_node_valid 0` | nœud non préparé ou carte changée : voir le journal du démarrage ; en conteneur, règle `device_cgroup_rules` absente |
| `collection_success 0` avec une erreur `EPERM` au journal | `userns-remap`, Docker rootless ou capacité `SYS_RAWIO` retirée |
| `hwraid_collector_supported 0` sur une carte | modèle non qualifié (`hwraid_controller_qualified 0`) sans `-allow-unqualified` : carte inventoriée seulement ; `cache_protection` seul : format de module de protection non décodé ; un autre collecteur seul : capacité non annoncée par la carte ou forme non implémentée |
| `hwraid_discovery_success 0` | sysfs partiellement illisible : voir l'avertissement `unreadable SCSI hosts` au journal |
| tous les collecteurs en échec avec `adapter paused` au journal | la carte se déclare en pause (opération de maintenance en cours) : l'exporter, comme l'outil du constructeur, ne l'interroge pas ; il réessaie au cycle suivant |
| tous les collecteurs en échec avec `revision refused` au journal | la carte refuse la vérification de révision (le journal donne `compatible false`) ou annonce un firmware antérieur au build 2120 (`firmware build` au journal) : l'outil du constructeur ne l'ouvrirait pas non plus. Build trop ancien : mettre à jour le firmware ; refus avec un build récent : vérifier avec l'outil du constructeur, qui affichera la même incompatibilité |
| `collection_success 0` avec `cannot allocate memory` au journal | sur toutes les commandes envoyées à la carte : réserve de commandes de gestion du pilote très probablement épuisée, voir « Limite du pilote aacraid » avant de redémarrer ; sur une seule lecture (par exemple la carte des connecteurs, commande de 2 048 octets), les autres réussissant : tampon de cette grande commande non alloué, ou réserve momentanément vide ; tant que les petites lectures de la même carte réussissent, la réserve n'est pas épuisée, même si l'erreur se répète. Seul l'échec des petites lectures de la même carte corrobore une réserve épuisée. Un échec de l'ouverture de la carte (704, 701, 703, 706) empêche toutes les autres lectures du cycle : le journal ne montre alors qu'une erreur |
| `hwraid_transfer_close_unconfirmed_total` augmente souvent | un autre outil (maxView, ARCCONF) interroge probablement la carte en même temps : sur le couple modèle et firmware validé, une lecture rejetée est refaite une fois dans le même cycle si ses morceaux étaient conformes et que le cycle n'est ni échu ni arrêté (sur une autre carte, elle n'est pas refaite, comme l'outil) ; sans conséquence tant que `collection_success` reste à 1 ; si des lectures échouent, réduire la fréquence de l'autre outil ou l'arrêter temporairement ([interface aacraid](aacraid-interface.md), section 4) |
| le conteneur s'arrête avec le code 3 | nœuds attendus changés depuis le démarrage : `restart: unless-stopped` le relance |
