# Métriques exposées

Référence des métriques publiées sur `/metrics`. Le contrat d'ensemble (principes, identité des contrôleurs, publication en cas d'échec) est à la section 5 du [cahier des charges](../cdc/cahier-des-charges-aacraid-exporter.md) ; ce document en est la liste exacte et à jour.

## Principes communs à tous les pilotes

Les métriques sont **les mêmes quel que soit le pilote, le contrôleur ou le constructeur**. Un module de pilote traduit ce que dit sa carte dans le vocabulaire commun ci-dessous ; les termes propres au constructeur ne sont publiés que dans des labels `vendor_*`, à titre d'information.

- **Identité.** Toutes les séries d'une carte portent `controller` : `serial:<numéro>` si le numéro de série matériel est valide et unique sur l'hôte, sinon `pci:<domaine:bus:slot.fonction>`. Une identité `serial:` établie est conservée tant que la carte reste à la même adresse PCI et que son numéro devient seulement illisible (attribut vide) : une lecture sysfs ratée ne renomme pas toutes ses séries. Un autre numéro désigne une autre carte. Au démarrage, sans identité établie, une carte au numéro illisible est publiée en `pci:`.
- **Objets locaux.** `volume` et `disk` sont des identifiants propres à la carte, stables tant que la configuration ne change pas : UID du conteneur en hexadécimal pour `volume` (aacraid), `d<numéro>` pour `disk`. Ils n'ont de sens qu'avec `controller`.
- **États.** Une série d'état vaut 1 et porte l'état observé dans le label `state`, pris dans un vocabulaire fermé (ci-dessous). Une seule série par objet à un instant donné.
- **Absence.** Une valeur non observée n'est pas publiée : lecture en échec, état non établi ou fonction non prise en charge. L'absence d'une série ne signifie jamais « tout va bien » : elle se surveille avec `hwraid_collection_success` et `hwraid_collector_supported`.
- **Identifiants matériels.** Les numéros de série et WWN sont publiés comme labels d'inventaire (`controller` pour la carte, `hwraid_disk_info` pour les disques) : ils permettent de corréler l'inventaire de la carte avec celui d'autres outils, par exemple smartctl_exporter. Ils n'apparaissent pas dans les journaux de l'exporter, et la documentation du dépôt n'utilise que des valeurs fictives.

## Vocabulaires normalisés

| Label | Valeurs | Publiées par aacraid aujourd'hui |
|---|---|---|
| `hwraid_volume_state` / `state` | `optimal`, `degraded`, `degraded_rebuilding`, `reduced_redundancy` et `reduced_redundancy_rebuilding` (volume à double redondance privé d'un membre, qui tolère encore une panne ; un assemblage en hérite de son volume membre le plus touché, sans garantie de tolérance pour lui-même), `failed`, `unknown` | toutes ; règle : [aacraid-interface.md](aacraid-interface.md), section 6 |
| `hwraid_disk_state` / `state` | `online`, `rebuilding`, `failed`, `ready` (disponible), `spare` (secours), `copyback` (copie de retour vers un disque remplacé), `passthrough` (disque « brut » pour la carte : JBOD, mode HBA ; sa présentation effective au système n'est pas vérifiée), `unsupported` (refusé par la carte) | toutes (observé sur matériel : `online`) ; règle : [aacraid-interface.md](aacraid-interface.md), section 6 bis |
| `hwraid_cache_protection_state` / `state` | `optimal`, `charging`, `failed`, `unknown` | toutes (`charging` et `optimal` non encore observés sur matériel). Priorité : un module installé sans le bit « prêt » est `failed`, quels que soient ses autres bits (cas du module en panne de l'hôte de référence, `0x40000081`) ; parmi les autres mots, toute combinaison de bits non qualifiée est `unknown`, jamais publiée comme saine |
| `hwraid_volume_info` / `raid_level` | `raid0`, `raid1`, `raid1e`, `raid5`, `raid6`, `raid10`, `raid50`, `raid60`, `concat` (concaténation de disques ou de volumes), `single` (volume posé sur un seul disque, sans découpage), `unknown` | toutes (observées sur matériel : `raid1`, `concat`) ; table de correspondance : [aacraid-interface.md](aacraid-interface.md), section 6 bis |
| `hwraid_volume_write_cache_policy` / `policy` | `write_back_always` (cache actif même sans protection), `write_back_when_protected` (actif seulement si le module de protection est opérationnel), `write_through` (désactivé), `unknown` | toutes (les trois réglages connus observés sur matériel ; `unknown`, avec la valeur brute dans `vendor_policy`, pour toute autre valeur) |
| `hwraid_cache_protection_present` / `type` | `flash_supercap` (supercondensateur et mémoire flash : ZMM/AFM, CacheVault, FBWC), `battery` (BBU) | `flash_supercap` |

`unknown` signifie que la carte a répondu, mais que l'exporter ne sait pas interpréter la réponse avec certitude (niveau non établi, migration, drapeau inconnu, assemblage incohérent). C'est un état à surveiller, pas une panne.

Les variantes propres à un constructeur se rattachent au terme commun le plus proche, leur nom restant dans `vendor_level` ou `vendor_state` (par exemple RAID 5EE → `raid5`, RAID 6 Reed-Solomon → `raid6`, « Raw (Smart HBA) » → `passthrough`).

## Métriques

### Exporter et découverte

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_exporter_build_info` | `version`, `revision`, `goversion` | 1 ; version de l'exporter (tag de la publication, sinon déduite du dépôt), commit source et version de Go |
| `hwraid_snapshot_ready` | — | 1 quand `/metrics` sert la dernière collecte ; 0, sans aucune valeur matérielle, avant la première collecte publiée, pendant l'arrêt de l'exporter, et quand la boucle de collecte n'a rien publié depuis plus de trois intervalles et un délai de cycle (vue périmée : boucle figée). Cette borne ne dit rien de la fraîcheur d'un état : Prometheus garde le dernier échantillon jusqu'au scrape qui le marque périmé, et une boucle figée peut laisser une seule observation remplir le délai `for` d'une règle ; `HwraidCollectionStale` et `hwraid_snapshot_ready` signalent ce cas |
| `hwraid_discovery_success` | — | 1 si la dernière découverte des cartes dans sysfs a abouti pour tous les hôtes SCSI ; 0 si sysfs ou au moins un hôte est illisible (une carte peut alors manquer à l'inventaire). Un hôte SCSI sans aucun ancêtre PCI (iSCSI, VMBus, hôte virtuel) est hors périmètre : ignoré, il ne met pas la découverte en échec |

### Contrôleur

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_controller_info` | `controller`, `identity_source` (`serial` ou `pci`), `pci_address`, `driver`, `backend`, `model`, `firmware` | 1 |
| `hwraid_controller_backend_supported` | `controller` | 1 si un module sait collecter cette carte |
| `hwraid_management_node_valid` | `controller` | 1 si le nœud de gestion dédié désigne bien la carte ; 0 s'il n'a pas pu être préparé ou vérifié, ou désigne une autre carte (erreur de déploiement). Absente pour une carte sans module et pour une carte dont la collecte a dépassé son délai pendant ce cycle (rien n'a été observé) |

### Collecte

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_collection_success` | `controller`, `collector` | 1 si la dernière lecture du collecteur a abouti ; 0 si elle a échoué, ou n'a pas pu être tentée parce qu'une lecture dont elle dépend a échoué dans le même cycle ; 0 aussi pour chaque collecteur pris en charge quand le nœud de gestion n'a pas pu être vérifié (prérequis de toute lecture), sauf un collecteur servi par une réponse gardée en mémoire qui a réussi sur la même carte (1, comme hors délai), les collecteurs connus comme non pris en charge restant publiés dans `hwraid_collector_supported`. Une carte dont une commande d'un cycle précédent est encore bloquée n'est pas relancée, et elle est publiée comme hors délai (0, ou 1 pour une réponse gardée ; voir « Collecte hors délai ») |
| `hwraid_collector_supported` | `controller`, `collector` | 0 si la carte ne permet pas ce collecteur ; absente sinon |
| `hwraid_collection_last_success_timestamp_seconds` | `controller`, `collector` | date Unix du dernier cycle où la lecture a abouti ; absente tant qu'elle n'a jamais abouti depuis le démarrage de l'exporter, et pour un collecteur servi par une réponse gardée en mémoire (`capabilities`, sauf si le numéro de série, le modèle ou le firmware de la carte est illisible : rien n'est alors gardé, la 706 est relue à chaque cycle et datée ; une carte dont un identifiant PCI est illisible n'est pas collectée du tout) |
| `hwraid_transfer_close_unconfirmed_total` | `controller` | compteur des lectures paginées rejetées parce que leur clôture a été confirmée par un statut non nominal (aacraid : `0xc5`, observé quand un autre outil, comme maxView, interroge la carte en même temps). Une lecture rejetée est refaite une fois dans le même cycle ; ses données ne sont jamais publiées. Une seconde tentative n'a lieu que si la première lecture n'a pas eu d'autre erreur et que le cycle n'est ni échu ni arrêté. Les rejets d'une collecte finie après son délai sont comptés pour la carte du cycle si la collecte est récoltée dans ce cycle ; sinon, quand la même adresse PCI est de nouveau collectée. Dans ce second cas, ils sont écartés si une autre carte est observée à cette adresse au cycle de leur récolte, mais comptés pour elle si le changement avait été constaté à un cycle antérieur (remplacement pendant une commande bloquée) ; si l'adresse n'est plus collectée, ils ne sont jamais comptés |
| `hwraid_collection_interval_seconds` | `controller` | intervalle de collecte effectif (option `-interval`), publié pour chaque carte prise en charge |

**Collecte hors délai.** Les cartes sont collectées en parallèle ; une carte qui n'a pas répondu dans le délai (`-collect-timeout`, 30 s par défaut ou l'intervalle s'il est plus court) est publiée avec ses collecteurs à 0, sans série d'état, et ses dates de dernier succès sont conservées. Exception : un collecteur servi par une réponse gardée en mémoire (`capabilities`) reste à 1 si sa dernière lecture a réussi sur la même carte, d'identité complète (tous les champs lus) et confirmée par une collecte effective depuis le dernier cycle où un champ était illisible. Tant que la commande bloquée n'a pas rendu la main, aucune nouvelle commande n'est envoyée à cette carte.

L'intervalle permet d'écrire des règles qui s'adaptent au réglage, par exemple « aucun succès depuis plus de trois cycles » : `time() - hwraid_collection_last_success_timestamp_seconds > on (job, instance, controller) group_left 3 * hwraid_collection_interval_seconds`. Joindre toujours aussi sur `job` et `instance` : deux hôtes peuvent publier le même identifiant `pci:`.

Collecteurs du module aacraid : `capabilities` (capacités de la carte), `volumes`, `disks`, `disk_inventory`, `disk_location`, `cache_protection`, `temperature` (non pris en charge si la carte n'annonce pas de capteur).

### Volumes

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_volume_info` | `controller`, `volume`, `name`, `raid_level`, `vendor_level`, `parent` | 1 ; `parent` est l'identifiant du volume qui assemble celui-ci, vide pour un volume de premier niveau |
| `hwraid_volume_state` | `controller`, `volume`, `state` | 1 |
| `hwraid_volume_capacity_bytes` | `controller`, `volume` | taille en octets |
| `hwraid_volume_member` | `controller`, `volume`, `member` | 1 ; `member` est un disque (`d<n>`) ou un volume (identifiant) |
| `hwraid_volume_write_cache_policy` | `controller`, `volume`, `policy`, `vendor_policy` | 1 ; réglage configuré du cache d'écriture, normalisé dans `policy` et dans les termes de l'outil du constructeur dans `vendor_policy` |
| `hwraid_volume_write_cache_enabled` | `controller`, `volume` | 1 si le cache d'écriture est actif à cet instant (écriture différée), 0 sinon ; absente si la carte rend une valeur non qualifiée |

Le réglage et l'état du cache d'écriture viennent de la même lecture que le reste du volume : aucune commande de plus. Le réglage dit ce que l'administrateur a demandé ; l'état dit ce que la carte fait, par exemple un cache coupé parce que le module de protection est en panne. Chaque volume, membres d'un assemblage compris, est lu et publié séparément ; sur l'hôte de référence, les volumes membres ont toujours eu le même réglage et le même état que leur volume de premier niveau, sans que rien ne le garantisse. Le cache de lecture n'est pas publié.

Une grappe à plusieurs niveaux (par exemple quatre miroirs assemblés) publie un volume par niveau. L'état d'un volume de premier niveau est le plus grave de ceux de ses volumes membres (un miroir dégradé rend l'assemblage dégradé, un miroir en échec le met en échec) ; la perte de redondance est aussi visible sur le volume membre.

### Disques

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_disk_state` | `controller`, `disk`, `state`, `vendor_state` | 1, pour chaque disque présent et pour chaque disque désigné par un volume mais absent ; un périphérique qui n'est pas un disque n'a un état (`failed`) que s'il est désigné par un volume ou porte le bit de secours ; `vendor_state` : libellé de l'outil constructeur (`Online`, `Hot Spare`, `Raw (Pass Through)`…), vide quand l'information du disque n'a pas pu être lue |
| `hwraid_disk_info` | `controller`, `disk`, `vendor`, `model`, `firmware`, `serial`, `wwn` | 1, pour chaque disque présent sur la carte, membre ou non (un périphérique qui n'est pas un disque, par exemple le fond de panier d'une baie, n'est pas publié) ; `wwn` au format `0x` suivi de 16 chiffres hexadécimaux, vide si la carte n'en donne pas |
| `hwraid_disk_capacity_bytes` | `controller`, `disk` | capacité brute en octets, absente si la taille de bloc n'est pas reconnue |
| `hwraid_disk_os_device` | `controller`, `disk`, `device`, `scsi_address` | 1, pour chaque disque que le pilote présente à Linux : nœud SCSI générique (`sg8`) et adresse SCSI Linux (`0:1:7:0`) |
| `hwraid_disk_location` | `controller`, `disk`, `connector`, `connector_name`, `device`, `phy` | 1, pour chaque disque relié directement à la carte : numéro et nom du connecteur, position dans le connecteur, phy de la carte |

L'inventaire est relu à chaque cycle : un disque remplacé au même emplacement change aussitôt de numéro de série.

Le lien avec Linux est lu dans sysfs à chaque cycle, sans commande à la carte. Il n'est publié que si le numéro de série gardé par le noyau (page VPD 0x80) est celui de la carte : un nœud `sgX` peut changer de numéro d'un démarrage à l'autre, la métrique suit. Pour interroger un disque avec `smartctl`, c'est le nœud à utiliser.

L'emplacement reprend la numérotation de l'outil du constructeur (« Connector n, Device m » d'ARCCONF et de maxView), fournie par la carte. Avec un câble éclaté mini-SAS vers 4 SATA, `device` m correspond à la fiche P(m+1) (vérifié sur l'hôte de référence). Un disque relié par un expandeur ou un fond de panier n'a pas d'emplacement publié. Le numéro de série, imprimé sur l'étiquette du disque, reste la confirmation avant de retirer un disque. Pour relier un disque à sa santé SMART, joindre sur le numéro de série, par exemple avec smartctl_exporter (label `serial_number` de `smartctl_device`). Le numéro de série n'est unique que sur un même hôte, et les deux exporters sont deux cibles distinctes (`instance` différentes) : la jointure porte donc aussi sur un label d'hôte commun, ici `host`, à poser par réétiquetage dans la configuration de Prometheus pour les deux cibles. Les numéros vides sont écartés :

```promql
hwraid_disk_info{serial!=""} * on (host, serial) group_left (device)
  label_replace(smartctl_device{serial_number!=""}, "serial", "$1", "serial_number", "(.+)")
```

La jointure exige une seule série `smartctl_device` par couple `(host, serial)`. Si smartctl_exporter voit le même disque par deux nœuds, filtrer avant la jointure celui à garder (par exemple `smartctl_device{device=~"sg.*"}`) ; si le nœud n'est pas utile, joindre sur `max by (host, serial) (…)` et retirer `device` de `group_left`.

L'état de tous les disques présents, membres ou non, s'appuie sur leur information (lecture 248). Si elle échoue pendant un cycle, seuls les disques membres d'un volume ont un état, déduit des drapeaux de leurs membres, avec un `vendor_state` vide. La santé SMART des disques est hors périmètre : elle relève d'outils dédiés.

### Protection du cache

| Métrique | Labels | Valeur |
|---|---|---|
| `hwraid_controller_temperature_celsius` | `controller` | température du contrôleur, en °C ; absente si la lecture n'a pas abouti dans le cycle |
| `hwraid_controller_temperature_threshold_celsius` | `controller`, `level` | seuils annoncés par la carte, en °C : `high`, à partir duquel l'outil du constructeur déclare un état anormal (100 sur la 6805), et `low` (95), dont l'outil ne se sert pas pour l'état affiché ; absente comme la température |
| `hwraid_cache_protection_present` | `controller`, `type`, `vendor_type` | 1 si un module est installé, 0 si la carte établit qu'elle n'en a pas ; absente si la présence n'est pas établie (aacraid : bits d'état sans le bit d'installation) |
| `hwraid_cache_protection_state` | `controller`, `state` | 1, si un module est installé, ou `unknown` si sa présence n'est pas établie |

## Métriques prévues

Prévues au cahier des charges, pas encore publiées :

| Métrique | Dépend de |
|---|---|
| `hwraid_task_progress_ratio` (reconstruction, vérification…) | décodage de la réponse aux requêtes de tâches (à capturer pendant une reconstruction) |
| `hwraid_controller_state` | lecture d'état de la carte |
| Emplacement des disques derrière un expandeur ou un fond de panier (boîtier, emplacement) | matériel correspondant et capture de l'outil témoin |

## Exemple

Hôte de référence (Adaptec 6805, quatre miroirs assemblés, module de protection du cache en panne), extrait :

```prometheus
hwraid_controller_info{controller="serial:XXXX",identity_source="serial",pci_address="0000:01:00.0",driver="aacraid",backend="aacraid",model="6805",firmware="5.3-0[19204]"} 1
hwraid_volume_info{controller="serial:XXXX",volume="1d000000",name="Array0",raid_level="concat",vendor_level="RAID_volume",parent=""} 1
hwraid_volume_info{controller="serial:XXXX",volume="1d000004",name="Array0",raid_level="raid1",vendor_level="1",parent="1d000000"} 1
hwraid_volume_state{controller="serial:XXXX",volume="1d000004",state="optimal"} 1
hwraid_volume_member{controller="serial:XXXX",volume="1d000004",member="d7"} 1
hwraid_volume_write_cache_policy{controller="serial:XXXX",volume="1d000000",policy="write_back_when_protected",vendor_policy="On when protected by battery/ZMM"} 1
hwraid_volume_write_cache_enabled{controller="serial:XXXX",volume="1d000000"} 0
hwraid_disk_state{controller="serial:XXXX",disk="d7",state="online",vendor_state="Online"} 1
hwraid_disk_info{controller="serial:XXXX",disk="d7",vendor="ATA",model="ST8000VN0022-2EL",firmware="SC61",serial="XXXXXXXX",wwn="0x5000c500xxxxxxxx"} 1
hwraid_disk_capacity_bytes{controller="serial:XXXX",disk="d7"} 8001563222016
hwraid_disk_location{controller="serial:XXXX",disk="d7",connector="1",connector_name="CN1",device="3",phy="4"} 1
hwraid_cache_protection_present{controller="serial:XXXX",type="flash_supercap",vendor_type="zmm"} 1
hwraid_cache_protection_state{controller="serial:XXXX",state="failed"} 1
hwraid_collection_success{controller="serial:XXXX",collector="volumes"} 1
```
