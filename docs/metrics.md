# Métriques exposées

Référence des métriques publiées sur `/metrics`. Le contrat d'ensemble (principes, identité des contrôleurs, publication en cas d'échec) est à la section 5 du [cahier des charges](../cdc/cahier-des-charges-aacraid-exporter.md) ; ce document en est la liste exacte et à jour.

## Principes communs à tous les pilotes

Les métriques sont **les mêmes quel que soit le pilote, le contrôleur ou le constructeur**. Un module de pilote traduit ce que dit sa carte dans le vocabulaire commun ci-dessous ; les termes propres au constructeur ne sont publiés que dans des labels `vendor_*`, à titre d'information.

- **Identité.** Toutes les séries d'une carte portent `controller` : `serial:<numéro>` si le numéro de série matériel est valide et unique sur l'hôte, sinon `pci:<domaine:bus:slot.fonction>`.
- **Objets locaux.** `volume` et `disk` sont des identifiants propres à la carte, stables tant que la configuration ne change pas : UID du conteneur en hexadécimal pour `volume` (aacraid), `d<numéro>` pour `disk`. Ils n'ont de sens qu'avec `controller`.
- **États.** Une série d'état vaut 1 et porte l'état observé dans le label `state`, pris dans un vocabulaire fermé (ci-dessous). Une seule série par objet à un instant donné.
- **Absence.** Une valeur non observée n'est pas publiée : lecture en échec, état non établi ou fonction non prise en charge. L'absence d'une série ne signifie jamais « tout va bien » : elle se surveille avec `hwraid_collection_success` et `hwraid_collector_supported`.
- **Identifiants matériels.** Seul `controller` peut porter un numéro de série (celui de la carte). Les numéros de série et WWN des disques ne sont pas publiés.

## Vocabulaires normalisés

| Label | Valeurs | Publiées par aacraid aujourd'hui |
|---|---|---|
| `hwraid_volume_state` / `state` | `optimal`, `degraded`, `degraded_rebuilding`, `failed`, `unknown` | toutes |
| `hwraid_disk_state` / `state` | `online`, `rebuilding`, `failed` | toutes |
| `hwraid_cache_protection_state` / `state` | `optimal`, `charging`, `failed` | toutes (`charging` et `optimal` non encore observés sur matériel) |
| `hwraid_volume_info` / `raid_level` | `raid0`, `raid1`, `raid1e`, `raid5`, `raid6`, `raid10`, `raid50`, `raid60`, `concat`, `unknown` | `raid1`, `concat`, `unknown` |
| `hwraid_cache_protection_present` / `type` | `flash_supercap` (supercondensateur et mémoire flash : ZMM/AFM, CacheVault, FBWC), `battery` (BBU) | `flash_supercap` |

`unknown` signifie que la carte a répondu, mais que l'exporter ne sait pas interpréter la réponse avec certitude (type de volume non qualifié, drapeau inconnu, propagation non établie). C'est un état à surveiller, pas une panne.

Valeurs réservées pour les modules à venir, non publiées aujourd'hui : `reduced_redundancy` et `reduced_redundancy_rebuilding` (perte d'un membre sur un RAID 6), `spare`, `ready`, `copyback` (disques).

## Métriques

### Exporter et découverte

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_snapshot_ready` | — | 0 avant la première découverte publiée, puis 1 |
| `hwraid_discovery_success` | — | 1 si la dernière découverte des cartes dans sysfs a abouti |

### Contrôleur

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_controller_info` | `controller`, `identity_source` (`serial` ou `pci`), `pci_address`, `driver`, `backend`, `model`, `firmware` | 1 |
| `hwraid_controller_backend_supported` | `controller` | 1 si un module sait collecter cette carte |
| `hwraid_management_node_valid` | `controller` | 1 si le nœud de gestion dédié désigne bien la carte ; 0 sinon (erreur de déploiement) |

### Collecte

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_collection_success` | `controller`, `collector` | 1 si la dernière lecture du collecteur a abouti, 0 sinon ; absente si elle n'a pas été tentée |
| `hwraid_collector_supported` | `controller`, `collector` | 0 si la carte ne permet pas ce collecteur ; absente sinon |
| `hwraid_collection_last_success_timestamp_seconds` | `controller`, `collector` | date Unix du dernier cycle où la lecture a abouti ; absente tant qu'elle n'a jamais abouti depuis le démarrage de l'exporter |
| `hwraid_collection_interval_seconds` | `controller` | intervalle de collecte effectif (option `-interval`), publié pour chaque carte prise en charge |

L'intervalle permet d'écrire des règles qui s'adaptent au réglage, par exemple « aucun succès depuis plus de trois cycles » : `time() - hwraid_collection_last_success_timestamp_seconds > on (controller) group_left 3 * hwraid_collection_interval_seconds`.

Collecteurs du module aacraid : `capabilities` (capacités de la carte), `volumes`, `disks`, `cache_protection`.

### Volumes

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_volume_info` | `controller`, `volume`, `name`, `raid_level`, `vendor_level`, `parent` | 1 ; `parent` est l'identifiant du volume qui assemble celui-ci, vide pour un volume de premier niveau |
| `hwraid_volume_state` | `controller`, `volume`, `state` | 1 |
| `hwraid_volume_capacity_bytes` | `controller`, `volume` | taille en octets |
| `hwraid_volume_member` | `controller`, `volume`, `member` | 1 ; `member` est un disque (`d<n>`) ou un volume (identifiant) |

Une grappe à plusieurs niveaux (par exemple quatre miroirs assemblés) publie un volume par niveau. L'état d'un volume de premier niveau vaut `unknown` quand l'un de ses volumes membres n'est pas optimal et que la propagation n'est pas établie pour ce type : la perte de redondance reste visible sur le volume membre.

### Disques

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_disk_state` | `controller`, `disk`, `state` | 1, pour chaque disque membre d'un volume dont l'état est établi |

Un disque présent qui n'est membre d'aucun volume (secours, disponible) n'est pas publié tant que son état n'est pas établi. La santé SMART des disques est hors périmètre : elle relève d'outils dédiés.

### Protection du cache

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_cache_protection_present` | `controller`, `type`, `vendor_type` | 1 si un module est installé, 0 si la carte n'en a pas |
| `hwraid_cache_protection_state` | `controller`, `state` | 1, seulement si un module est installé |

## Métriques prévues

Prévues au cahier des charges, pas encore publiées :

| Métrique | Dépend de |
|---|---|
| `hwraid_task_progress_ratio` (reconstruction, vérification…) | décodage de la réponse aux requêtes de tâches (à capturer pendant une reconstruction) |
| `hwraid_controller_state`, `hwraid_controller_temperature_celsius` | lecture d'état et des capteurs de la carte |
| `hwraid_disk_info` | lecture d'inventaire des disques, sans numéro de série ni WWN |
| `hwraid_volume_write_cache_enabled` | position de l'état du cache d'écriture dans la lecture de volume |

## Exemple

Hôte de référence (Adaptec 6805, quatre miroirs assemblés, module de protection du cache en panne), extrait :

```prometheus
hwraid_controller_info{controller="serial:XXXX",identity_source="serial",pci_address="0000:01:00.0",driver="aacraid",backend="aacraid",model="6805",firmware="5.3-0[19204]"} 1
hwraid_volume_info{controller="serial:XXXX",volume="1d000000",name="Array0",raid_level="concat",vendor_level="RAID_volume",parent=""} 1
hwraid_volume_info{controller="serial:XXXX",volume="1d000004",name="Array0",raid_level="raid1",vendor_level="1",parent="1d000000"} 1
hwraid_volume_state{controller="serial:XXXX",volume="1d000004",state="optimal"} 1
hwraid_volume_member{controller="serial:XXXX",volume="1d000004",member="d7"} 1
hwraid_disk_state{controller="serial:XXXX",disk="d7",state="online"} 1
hwraid_cache_protection_present{controller="serial:XXXX",type="flash_supercap",vendor_type="zmm"} 1
hwraid_cache_protection_state{controller="serial:XXXX",state="failed"} 1
hwraid_collection_success{controller="serial:XXXX",collector="volumes"} 1
```
