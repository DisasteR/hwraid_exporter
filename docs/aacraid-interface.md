# Interface de gestion aacraid : ce qu'utilise l'exporter

Ce document décrit l'interface entre l'exporter et une carte Adaptec gérée par le pilote Linux `aacraid` (première cible : Adaptec 6805, firmware 5.3-0[19204]). Il complète la section 3 du cahier des charges.

Chaque élément porte sa provenance :

- **[public]** : documenté dans une source publique, citée ;
- **[observé]** : établi en observant, sur l'hôte de référence, les échanges entre l'outil constructeur et le pilote, puis en les comparant à ce que l'outil affiche. Leur domaine de validité est donné à la section 7.
- **[interopérabilité]** : établi par l'analyse de l'outil constructeur, faute de source publique, au titre de l'exception d'interopérabilité (directive 2009/24/CE, article 6 ; code de la propriété intellectuelle, article L122-6-1 IV ; textes et limites : [base-legale-interoperabilite.md](base-legale-interoperabilite.md)). Seul le résultat nécessaire à l'interopérabilité de l'exporter est décrit ici ; la méthode et les éléments internes de l'outil ne sont pas publiés. Même domaine de validité (section 7).

Tous les entiers sont en little-endian. Les positions sont en octets, à partir de 0.

## 1. Accès au pilote [public]

| Élément | Valeur | Source |
|---|---|---|
| Nœud caractère | major dynamique « aac » dans `/proc/devices`, minor = `unique_id` de l'hôte SCSI | `drivers/scsi/aacraid/linit.c` (`register_chrdev`, `aac_cfg_open`) |
| Envoi d'une commande firmware | `FSACTL_SENDFIB` = `0x42008` ; `FSACTL_SEND_LARGE_FIB` = `0x42168` | `aacraid.h` (`CTL_CODE`), `commctrl.c` (`ioctl_send_fib`) |
| Plage copiée par le pilote | `max(Size + 32, SenderSize)` octets, au plus 2 048, à l'aller et au retour | `commctrl.c` (`ioctl_send_fib`) |
| Informations de la carte sans commande firmware | `FSACTL_GET_HBA_INFO` = `0x42198`, réponse de 200 octets construite par le pilote ; numéro de carte, bus, slot, fonction et quatre identifiants PCI comparés à sysfs avant toute commande firmware (vérifié sur l'hôte de référence le 2026-11-23) | `commctrl.c` (`aac_get_hba_info`), `aacraid.h` (`struct aac_hba_info`) |
| Droits | `CAP_SYS_RAWIO` à chaque ioctl | `linit.c` (`aac_cfg_ioctl`) |
| Disques présentés à Linux | disque de la carte (canal c, cible t) présenté à l'adresse SCSI Linux (H, c + 1, t, 0), le canal 0 portant les volumes ; vérifié sur l'hôte de référence (8 disques, numéros de série de la page VPD 0x80 identiques à ceux de la lecture 248) | `aacraid.h` (`aac_phys_to_logical`) |
| Sérialisation | un mutex par carte pour chaque ioctl (relâché pendant `FSACTL_RESET_IOP`, que l'exporter n'emploie pas) | `commctrl.c` (`aac_do_ioctl`) |

## 2. Bloc de commande firmware (FIB) [public]

En-tête de 32 octets (`struct aac_fibhdr`, `aacraid.h`) :

| Octets | Champ |
|---|---|
| 0-3 | `XferState` |
| 4-5 | `Command` |
| 6 | `StructType` |
| 7 | inutilisé |
| 8-9 | `Size` : dans la requête passée à l'ioctl, taille des données qui suivent l'en-tête ; le pilote y ajoute ensuite les 32 octets d'en-tête, et la réponse porte la taille totale |
| 10-11 | `SenderSize` |
| 12-31 | champs techniques (adresses, poignée, chaînage) |

Commandes d'en-tête utilisées par l'exporter :

| `Command` | Nom | Source |
|---|---|---|
| 500 | `ContainerCommand` | Linux `aacraid.h`, FreeBSD `aacraid_reg.h` |
| 700 | `AifRequest` | idem |
| 701, 703, 704 | `CheckRevision`, `RequestAdapterInfo`, `IsAdapterPaused` | FreeBSD `aacraid_reg.h` |
| 706 | `RequestSupplementAdapterInfo` | Linux `aacraid.h` (`struct aac_supplement_adapter_info`), FreeBSD `aacraid_reg.h` |
| 803 | `AdapterEnvironmentInfo` (capteurs de température) | FreeBSD `aacraid_reg.h` |

Pour `ContainerCommand`, les données commencent par la commande VM (mot 0). Avec `VM_ContainerConfig`, le mot 1 est la sous-commande ; avec `VM_NameServe` et `VM_NameServe64`, c'est le type de la requête (`struct aac_query_mount`). Valeurs publiques utilisées : `VM_ContainerConfig` = 2, `VM_NameServe` = 1, `VM_NameServe64` = 22 ; sous-commandes `CT_GET_CONTAINER_COUNT` = 4, `CT_GET_SCSI_METHOD` = 64, `CT_CID_TO_32BITS_UID` = 165 (Linux `aacraid.h` et `aachba.c`, FreeBSD `aacraid_reg.h`).

Pour `AifRequest`, le mot 0 des données est la sous-commande : `AifReqJobsForCtr` = 101 (tâches d'un conteneur), `AifReqJobsForScsi` = 102 (tâches d'un disque). La même commande porte des sous-commandes d'action (arrêt, suspension, reprise, lancement de tâche) : la liste blanche de l'exporter contrôle la sous-commande, jamais seulement `Command`.

## 3. Volumes, membres et disques : données publiques [public]

Le pilote Linux interroge lui-même chaque conteneur au démarrage (`VM_NameServe`, `VM_NameServe64`, `CT_CID_TO_32BITS_UID` dans `aachba.c`) et publie le résultat dans sysfs :

- `/sys/class/scsi_device/H:0:T:0/device/level` : type du conteneur (table `container_types` de `aachba.c` : `Volume`, `Mirror`, `Stripe`, `RAID5`, `RAID10`, `RAID6`…) ;
- `/sys/class/scsi_host/hostH/` : `model`, `hba_kernel_version`, `serial_number`, `unique_id`.

Le nom, la capacité, le type et l'identifiant unique d'un conteneur sont donc disponibles par la voie publique, mais seulement pour les volumes présentés à Linux et sans leurs membres. L'exporter lit ces informations, avec la composition des volumes, par l'information de conteneur (234) et la liste des membres (249), qui décrivent aussi les volumes internes d'un assemblage.

## 4. Transfert paginé [observé]

Les informations détaillées sont renvoyées par un transfert en plusieurs FIB, toutes de type `ContainerCommand` / `VM_ContainerConfig`. En-tête de chaque requête : `XferState` = `HostOwned | ApiFib` (`0x00100001`), `StructType` = 1, `Size` = 480, `SenderSize` = 512. Données (huit mots, reste nul) :

| Étape | Mots 0 à 7 de la requête | Réponse |
|---|---|---|
| Ouverture | `2`, sous-commande, paramètre, `0`, marque, `0xffffffff`, `0`, taille | statut, écho, `0xc9`, taille totale, `0xda`, identifiant du transfert |
| Morceau n | `2`, `83`, `0xc9` (n = 0) ou `0xc8`, taille totale, `0xda`, identifiant, n, taille totale | les huit mots, puis **432 octets de données** ; `Size` = 496, en-tête compris |
| Clôture | `2`, `84`, `0xc8`, taille totale, `0xda`, identifiant, dernier n lu, taille totale | `0xc6` au mot 2 ; `0xc5` observée parfois (voir ci-dessous) |

| Lecture | Sous-commande | Paramètre | Marque | Taille |
|---|---|---|---|---|
| Liste des conteneurs | 229 | 0 | `0xda` | 1 032 |
| Information de conteneur | 234 | numéro du conteneur | `0xda` | 4 096 |
| Liste des disques présents | 247 | 0 | 0 | 4 100 |
| Information de disque | 248 | numéro du disque | 0 | 4 096 |
| Liste des membres | 249 | UID du conteneur | 0 | 8 + 32 × nombre de membres |

L'outil de référence laisse au-delà des huit mots des résidus de ses propres tampons ; ils ne font pas partie de la requête et l'exporter envoie des zéros. Une réponse de morceau porte toujours la marque `0xc8` et `Size` = 496 : 32 octets d'en-tête, 32 de mots de contrôle et 432 de données. Le tampon copié par le pilote fait 512 octets, mais la carte n'en écrit pas les 16 derniers : sur les 325 réponses de morceau capturées, ils sont toujours nuls et identiques à la requête, y compris dans les transferts denses. Le pas est donc de 432 octets par morceau ; l'exporter exige la marque et la valeur de `Size`, et n'utilise jamais les octets non écrits.

**Code de la réponse d'ouverture.** L'exporter n'engage un transfert que si le mot 4 de la réponse d'ouverture vaut `0xda` (CT_OK), valeur de toutes les ouvertures capturées pour les lectures 229, 234, 247, 248 et 249 (315 réponses). L'outil de référence accepte aussi `0x11a` pour la lecture 248, d'après son code (lu dans la version 3.07 ; la 2.05 fait la même comparaison, branche non suivie) ; cette valeur n'a jamais été observée et son sens n'est pas établi. L'exporter la refuse : la lecture est alors en échec, visible par `hwraid_collection_success`, plutôt que d'utiliser des données d'un statut non qualifié.

**Clôture confirmée par `0xc5`.** Sur l'hôte de référence, pendant que maxView interrogeait la carte, deux clôtures sur environ 23 cycles (environ 1 400 transferts) ont été confirmées par `0xc5` au lieu de `0xc6`, toujours avec l'identifiant de notre transfert et après des morceaux conformes ; la marque n'apparaît dans aucune capture de l'outil témoin. Les mots de statut des réponses de pagination sont des codes de statut de commande : les sources publiques n'en nomment qu'un, `CT_OK` = 218 (`0xda`, Linux `aacraid.h`, FreeBSD `aacraid_reg.h`) ; 197 (`0xc5`) et 198 (`0xc6`) n'y ont pas de nom. L'outil de référence rend `CT_OK` après une clôture `0xc6` et le code reçu dans les autres cas ; les fonctions qui demandent les lectures 229, 234, 247, 248 et 249 n'utilisent les données que pour `CT_OK` : **une clôture `0xc5` invalide la lecture** [interopérabilité]. L'exporter fait de même : il rejette la lecture, la refait une fois dans le même cycle, compte le rejet (`hwraid_transfer_close_unconfirmed_total`), et signale une erreur si le second essai est aussi rejeté. L'outil de référence (versions 2.05 et 3.07, vérifiées) ne refait pas la lecture rejetée ; la relecture unique est un choix de l'exporter : elle n'a lieu que si les morceaux de la lecture rejetée étaient conformes. Après une erreur de morceau, la lecture est en échec sans nouvel essai, le rejet étant compté. La cause exacte de `0xc5` n'est pas établie ; un croisement avec les transferts de maxView est probable. Une seconde piste n'est pas écartée : avant la v0.1.2, l'exporter pouvait être interrompu par ses propres signaux pendant une commande, et le pilote renvoie alors la commande à la carte, qui la reçoit deux fois ([défaut du pilote](aacraid-defaut-pilote.md#mécanisme)). Le 2026-11-24, la v0.1.2 n'a observé aucune `0xc5` en 30 cycles, maxView et ARCCONF interrogeant la carte en même temps ; à ce rythme, on en attendait environ une : ce résultat ne départage pas les deux pistes. Si `hwraid_transfer_close_unconfirmed_total` continue d'augmenter avec la v0.1.2, la seconde piste est affaiblie pour l'exporter, mais pas pour les autres outils : ARCCONF ou maxView interrompus peuvent eux aussi faire envoyer une commande deux fois. Seule une capture des commandes de chaque outil au moment d'une `0xc5` départagerait les pistes.

L'exporter ne lit que les morceaux nécessaires aux champs qu'il décode, puis clôture le transfert : sur l'hôte de référence, un seul morceau suffit pour chaque lecture. La clôture après le morceau 0 est une forme employée par l'outil de référence ; le firmware de l'hôte de référence l'accepte aussi pour les lectures de 4 096 octets (vérifié le 2026-11-23).

Le pilote sérialise chaque ioctl, pas un transfert entier : un autre programme peut intercaler ses propres commandes entre deux morceaux. L'exporter vérifie à chaque réponse le statut (mot 0), l'écho de la sous-commande (mot 1), l'identifiant du transfert et le numéro du morceau ; il refuse une taille totale supérieure à 4 100 octets. Au premier écart sur un morceau, il clôture le transfert et rejette le relevé entier du cycle : aucune configuration partielle n'est publiée. Une ouverture non conforme (réponse tronquée ou mal formée, statut d'échec, marque autre que `0xc9`, code autre que `CT_OK`, taille nulle ou excessive) n'est suivie d'aucune commande, pas même d'une clôture : rien ne prouve qu'un transfert existe sous l'identifiant reçu, et clore un transfert peut-être étranger serait pire que de laisser au firmware l'unique transfert éventuellement ouvert. La lecture est en échec, avec la même conséquence pour le relevé.

## 5. Formats observés [observé]

### Liste des conteneurs (sous-commande 229, paramètre : 0)

| Octets | Champ |
|---|---|
| 4-7 | nombre de conteneurs |
| 8-… | un numéro de conteneur par mot |

Sur l'hôte de référence : 5 conteneurs, numéros 0, `0xfc`, `0xfd`, `0xfe`, `0xff`.

### Liste des disques présents (sous-commande 247, paramètre : 0)

| Octets | Champ |
|---|---|
| 0-3 | nombre de disques |
| 4-… | un numéro de disque par mot |

### Information de volume (sous-commande 234, paramètre : numéro de conteneur)

| Octets | Champ | Valeur de référence |
|---|---|---|
| 16-19 | identifiant unique | égal au « Unique Identifier » affiché par l'outil témoin |
| 48-51 | type, dans la numérotation choisie par les octets 96-103 (section 6 bis) : 1 = assemblage de conteneurs (« RAID_volume » pour l'outil témoin, concaténation de volumes), 3 = miroir de deux disques (RAID 1) ; seuls types observés | 1 (volume), 3 (miroirs) |
| 52-55 | UID du conteneur qui l'assemble, `0xffffffff` sinon | `0xffffffff` (volume), UID du volume (miroirs) |
| 56-59 | nombre de membres | 4 (volume), 2 (miroirs) |
| 64-71 | taille en blocs de 512 octets | 62 495 109 120 (= 30 515 190 Mio) |
| 148-151 | réglage du cache d'écriture : 2 = « Enabled » (toujours actif, `wb` dans ARCCONF), 1 = « On when protected by battery/ZMM » (`wbb`), 0 = « Disabled » (`wt`) [observé] | 1 |
| 156-159 | état effectif du cache d'écriture : 1 = « On », 0 = « Off » [observé] | 0 (module de protection en panne) |
| 296-311 | nom, chaîne terminée par un octet nul, lue sur 16 octets au plus (longueur maximale non établie) | `Array0` |

**Cache d'écriture.** Positions établies en changeant le réglage avec l'outil témoin et en comparant les lectures de chaque état, sur les cinq conteneurs de l'hôte de référence (module de protection en panne) : réglage « Enabled », état « On » ; réglage « On when protected », état « Off » ; réglage « Disabled », état « Off ». Le passage du deuxième au troisième ne change que l'octet 148 : c'est le réglage. L'octet 156 est le seul autre octet qui ait changé : c'est l'état. Le passage de l'état à « On » avec le réglage « On when protected » et un module opérationnel reste à observer. Une autre valeur de réglage est publiée `unknown` ; une autre valeur d'état n'est pas publiée. Les octets 144 et 152 valent 1 dans toutes les captures, quand l'outil affiche le cache de lecture « Enabled » et « On » ; ils ne sont pas utilisés.

L'exporter publie le niveau RAID de `hwraid_volume_info` dans le vocabulaire normalisé ([metrics.md](metrics.md)) et, dans `vendor_level`, le libellé de l'outil témoin, selon la table de la section 6 bis : sur l'hôte de référence, type 3 → `raid_level="raid1"`, `vendor_level="1"` ; type 1 assemblant des miroirs → `raid_level="concat"`, `vendor_level="RAID_volume"`. Un type sans niveau dans la table, ou soumis à un traitement particulier (migration, types classiques 5, 6 et 7), donne `raid_level="unknown"` et `vendor_level="type N"` (`"classic type N"` en numérotation classique) : c'est plus prudent que l'outil, qui peut alors afficher le niveau d'un conteneur associé. Les libellés de `vendor_level` sont ceux de la version 2.05 de l'outil (la 3.07 affiche « 6 » au lieu de « 6 Reed-Solomon »). Les variantes 5EE et Reed-Solomon sont publiées `raid5`, `raid6` ou `raid60`, la variante restant dans `vendor_level` ; un volume simple donne `single` (`Simple_volume`), un volume étendu `concat` (`Spanned_volume`), à condition que tous leurs membres soient des disques : un membre conteneur qui ne fait pas un assemblage donne `unknown`. Le niveau n'est publié que si les membres lui correspondent : hors assemblage, tous les membres sont des disques, au moins aussi nombreux que le minimum de la table de création de l'outil (RAID 0, 1, volume étendu : 2 ; RAID 1E et 5 : 3 ; RAID 5EE, 6 et 10 : 4 ; RAID 50 : 6 ; RAID 60 : 8) ; un assemblage compte au moins deux membres. Sinon : `unknown`. Les RAID 10 et 50 de numérotation classique que l'outil construit sur des grappes, forme non observée, sont donc publiés `unknown`. Un assemblage exige au moins un membre qui soit un autre conteneur lu dans le cycle : un conteneur dont le seul membre conteneur est lui-même, ou un type classique 12 (volume de miroirs) fait de disques, donne `unknown`. Ce contrôle ne cherche pas les boucles plus longues (deux volumes qui se désignent mutuellement gardent leur niveau) ; le calcul de l'état, lui, détecte toute boucle et la rend indéterminée (section 6).

**Principe.** L'exporter publie le niveau que la carte déclare, après ces contrôles de cohérence de la réponse elle-même (nature des membres, minimum de disques établi pour les cartes `aacraid`, membre conteneur distinct du conteneur lui-même pour un assemblage). Il ne revalide pas la géométrie de création des volumes (parité, multiples, maximums) : l'outil de référence ne la revalide pas non plus quand il affiche une configuration, et la carte reste la source de vérité.

### Liste des membres (sous-commande 249, paramètre : UID du conteneur)

En-tête de 8 octets (mot 1 = nombre de membres), puis une entrée de 32 octets par membre :

| Octets | Champ |
|---|---|
| 0-3 | numéro du disque membre, `0xffffffff` si le membre est un conteneur |
| 4-7 | UID du conteneur membre, `0xffffffff` si le membre est un disque |
| 10-11 | rang du membre : ordre de parcours de l'outil de référence, qui ignore une entrée de rang supérieur ou égal au nombre de membres ; l'exporter refuse une telle liste, ainsi qu'une liste aux rangs répétés |
| 12-15 | état du membre (section 6) [interopérabilité] |
| 16-23 | bloc de début du membre sur le disque |
| 24-31 | taille du membre en blocs |

Sur l'hôte de référence, le volume visible assemble quatre miroirs, eux-mêmes listés avec leurs deux disques, dans l'ordre exact affiché par l'outil témoin. Le numéro de disque vaut connecteur × 4 + device sur ce modèle.

### Information de disque (sous-commande 248, paramètre : numéro de disque)

Lue pour chaque disque de la liste 247 (collecteur `disk_inventory`). Les champs texte ont une largeur fixe, complétés par des espaces et sans séparateur, comme dans une réponse INQUIRY SCSI.

| Octets | Champ |
|---|---|
| 8-11 | numéro du disque (vérifié égal au paramètre) |
| 40-47 | capacité brute en blocs |
| 48-55 | taille utilisable en blocs |
| 64-67 | taille de bloc en octets |
| 88-95 | fabricant |
| 96-111 | modèle |
| 112-115 | version de firmware |
| 116-135 | numéro de série, terminé par un octet nul s'il est plus court |
| 200 | phy de la carte auquel le disque est relié (même valeur à l'octet 191) |
| 288-295 | WWN, dans l'ordre de l'identifiant (`0x5000c500…`) |

Vérifié sur les huit disques de l'hôte de référence contre l'outil témoin (même capture) : fabricant, modèle, firmware, numéro de série, WWN et capacité brute identiques. Lecture par l'exporter validée le 2026-11-23 sur l'hôte de référence (huit disques, valeurs identiques). Le numéro de série figure aussi aux octets 304-327, cadré à droite ; il n'est pas utilisé.

Le phy concorde avec « Attached PHY Identifier » de l'outil témoin pour les huit disques. La réponse ne contient pas d'emplacement (connecteur, position) : il est donné par la carte des connecteurs, ci-dessous.

### Carte des connecteurs (commande 708, `FSACTL_SEND_LARGE_FIB`)

Commande FIB 708 (`RequestConnectorMap`), envoyée une fois par l'outil témoin à chaque consultation, par `FSACTL_SEND_LARGE_FIB` sur un tampon de 2 048 octets. Requête : `XferState` = HostOwned | ApiFib, `Command` = 708, `StructType` = 1, `Size` = 4, `SenderSize` = 2 048, données nulles. Réponse : `Size` = 192, en-tête compris, sur l'hôte de référence.

| Octets (données) | Champ |
|---|---|
| 0-3 | 1 sur l'hôte de référence (seule valeur acceptée) |
| 4 | nombre d'entrées de disque |
| 5 | nombre de connecteurs (2 sur l'hôte de référence, seule valeur acceptée) |
| 8 + 12 × c | connecteur c : numéro (octet 0), nom sur 8 octets à partir de l'octet 4 (`CN0`, `CN1`) |
| 32 + 16 × i | entrée i : désignation du disque comme dans la liste 247 (canal × 2²⁴ + cible, mot 0), numéro de connecteur (octet 4), position dans le connecteur (octet 5), phy de la carte (octet 6), adresse SAS de la carte (octets 8-15) |

L'outil témoin affiche la position telle quelle (« Connector n, Device m ») pour le disque dont l'entrée correspond, sans calcul. Un disque qui n'est pas relié directement à la carte (expandeur, fond de panier) n'a pas d'entrée : l'outil l'affiche alors par boîtier et emplacement, lus autrement ; l'exporter ne publie pas d'emplacement pour ce disque. Sur l'hôte de référence, le phy d'une entrée est celui de la lecture 248 du même disque : l'exporter exige cette concordance avant de publier un emplacement.

Vérifié sur les huit disques de l'hôte de référence contre l'outil témoin (« Reported Location » et « Attached PHY Identifier »), et sur le câblage réel le 2026-11-23 : avec des câbles éclatés mini-SAS vers 4 SATA, la position m correspond à la fiche P(m+1). Lecture par l'exporter validée le 2026-11-23 sur l'hôte de référence (huit emplacements identiques à l'outil témoin).

### Informations de la carte (commande 703, `RequestAdapterInfo`) [public]

Requête de la même forme que la 706 : `XferState` = HostOwned (1), `Size` = 4, un mot de données nul, `SenderSize` = 512. Réponse : `struct aac_adapter_info` (FreeBSD `aacraid_reg.h`), `Size` = 120 (88 octets de données) sur l'hôte de référence. L'exporter ne lit que `SupportedOptions` (octets 80-83 des données), dont le bit `AAC_SUPPORTED_HEAT_SENSOR` (`0x80000`) annonce un capteur de température ; valeur sur l'hôte de référence : `0x141f1d7c`, bit positionné. La réponse porte aussi le numéro de série de la carte (octets 68-75) : l'exporter ne le lit pas. Lue une fois par carte et gardée en mémoire, sous la même clé que la 706 ; relue à chaque cycle tant que l'identité de la carte est incomplète (rien n'est alors gardé).

### Température de la carte (commande 803, `AdapterEnvironmentInfo`) [interopérabilité]

L'outil de référence (versions 2.05 et 3.07) n'envoie cette commande que si `AAC_SUPPORTED_HEAT_SENSOR` est annoncé par la 703 ; l'exporter fait de même : si la 703 aboutit sans annoncer de capteur, le collecteur `temperature` est publié comme non pris en charge ; si la 703 échoue, la lecture est en échec (`hwraid_collection_success` 0) et retentée au cycle suivant. Requête : `XferState` = `HostOwned | ApiFib` (`0x00100001`, Linux `aacraid.h`), comme dans la requête de l'outil de référence et dans ses lectures de la protection du cache (ses requêtes 703 et 706 n'ont que HostOwned), `Size` = 4, mot de données 0 = 1, reste nul, `SenderSize` = 512. Réponse, champs de 16 bits lus par l'outil de référence et nommés d'après sa sortie JSON :

| Octets (données) | Champ | Hôte de référence |
|---|---|---|
| 0-3 | statut (0 : succès) | 0 |
| 4-… | nom du capteur (texte) | « Analog Device ADM1032 » |
| 36-37 | température en °C (`heatSensorTemperature`) | 45 à 51 |
| 40-41 | seuil bas (`heatSensorThresholdLo`) ; l'outil de référence ne l'utilise pas pour l'état affiché | 95 |
| 42-43 | seuil haut (`heatSensorThresholdHi`) : l'outil de référence affiche un état anormal quand la température l'atteint | 100 |

Les données suivantes ne sont pas interprétées. Concordance avec l'affichage de l'outil de référence dans les mêmes captures : 49 et 48 °C. Lue à chaque cycle.

### Statistiques du contrôleur (sous-commande 244, paramètre : 0) [observé]

Transfert paginé (section 4) de 2 788 octets sur l'hôte de référence, quand la collecte des statistiques est activée sur la carte. Compteurs cumulés depuis la dernière remise à zéro, entiers de 8 octets. Positions dans les données reconstituées au pas de 432 octets ; celles du troisième morceau ont été corrigées le 2026-11-23 (relevé initial au pas de 448, décalé de 32 octets). Le relevé des valeurs contre l'outil témoin reste à refaire avant toute publication de ces compteurs :

| Octets | Champ |
|---|---|
| 8-11 | secondes écoulées depuis la dernière remise à zéro |
| 16-103 | commandes en attente : 11 classes (0, 1, 2, 4 … 512) |
| 1104 | lectures de l'hôte (volume 0) |
| 1112 | écritures de l'hôte |
| 1120 | secteurs lus |
| 1128 | secteurs écrits |
| 1152, 1160 | lectures et écritures hors cache |
| 1168 | lectures servies entièrement par le cache DRAM |
| 1176 | lectures en attente d'un chargement ou d'un vidage |
| 1184 | lectures servies en partie par le cache |
| 1192 | écritures absorbées par le cache DRAM |
| 1200 | écritures en attente d'un chargement ou d'un vidage |
| 1208 | vidages du cache DRAM |

Le même transfert porte des histogrammes par volume (temps de complétion, intervalle entre entrées-sorties, taille, déplacement) et des compteurs d'erreurs par disque ; leurs positions restent à établir. Les positions des volumes suivants le premier ne sont pas établies (un seul volume sur l'hôte de référence).

## 6. État des membres, des volumes et des disques [interopérabilité]

### État d'un membre

Les octets 12 à 15 d'une entrée de la liste des membres portent l'état du membre :

| Bit | Sens |
|---|---|
| `0x2` | membre en cours de reconstruction |
| `0x4` | membre défaillant |
| aucun | membre normal |

Si les deux bits sont positionnés, le membre est traité comme en reconstruction. Un autre bit positionné rend l'état du membre **indéterminé** (l'outil de référence l'ignorerait), sauf preuve de perte (ci-dessous). Ces drapeaux ne sont lus que pour les membres disques ; un membre conteneur d'un assemblage tient son état de son propre calcul. Sur l'hôte de référence, les 16 entrées ont ces octets à 0.

Un membre est **perdu**, quels que soient ses drapeaux, si le disque qu'il désigne (octets 0-3) n'apparaît pas dans la liste des disques présents, ou si l'information de ce disque (248) le déclare en panne (bit `0x1` de l'octet 68, section 6 bis) ou montre qu'il n'est pas un disque (section « État d'un disque ») : comme dans l'outil de référence, la panne l'emporte sur la reconstruction. Écart prudent : l'outil peut afficher « non pris en charge » un disque qui porte aussi le bit de panne (section 6 bis) et ne plus le compter perdu ; l'exporter le compte perdu et le publie en panne. La liste des disques est lue complètement dans le même cycle ; l'information des disques est lue avant le calcul des états. Si l'une de ses lectures échoue, les réponses déjà lues dans le cycle comptent comme preuves de panne, et les disques non lus sont évalués sans elle (l'échec reste visible par `hwraid_collection_success{collector="disk_inventory"}`).

### État d'un conteneur

L'état d'un conteneur ne figure pas tel quel dans les réponses : il se calcule à partir de ses membres et de son niveau, tel qu'il est publié (section 5), selon la règle de l'outil de référence (section 6 bis), avec deux écarts plus prudents. Seule la numérotation ARCIO est évaluée.

- **Volume de disques** (tout niveau publié autre qu'un assemblage). Le nombre k de membres perdus compte les membres défaillants et **tous** les membres en reconstruction (écart : hors double redondance, l'outil ne compte un membre en reconstruction que si aucun membre n'a encore été compté quand il le parcourt, si bien que son résultat dépend de l'ordre des membres ; en double redondance, il le compte toujours), sauf quand les octets 120-123 du conteneur valent 7 ou 8 et ses octets 128-131 ne sont pas nuls : les membres en reconstruction ne sont alors pas comptés, comme dans l'outil. Puis :

  | Famille de niveaux | k = 0 | k = 1 | k = 2 | k ≥ 3 |
  |---|---|---|---|---|
  | Sans redondance : `raid0`, `single`, `concat` de disques | optimal | en échec | en échec | en échec |
  | Simple redondance : `raid1`, `raid1e`, `raid5`, `raid10`, `raid50` | optimal | dégradé | en échec | en échec |
  | Double redondance : `raid6`, `raid60` | optimal | redondance réduite | dégradé | en échec |

  Les états « dégradé » et « redondance réduite » prennent leur variante « en reconstruction » si un membre du volume est en reconstruction (sur un disque ni absent ni en panne) et qu'aucun membre n'est mort (écart prudent : un membre mort reste à remplacer ; l'outil garde parfois la variante, selon l'ordre de parcours des disques). Deux membres sur le même disque rendent l'état **indéterminé**. Un membre au drapeau inconnu (peut-être perdu) aussi, sauf si les pertes avérées des autres membres exigent déjà une action : volume **en échec** si elles dépassent la tolérance, **dégradé** si elles le rendent dégradé ou dégradé en reconstruction (la variante est retirée, le membre inconnu pouvant la démentir). Comme l'outil, la règle ne tient pas compte des grappes : un RAID 10 ou 50 à deux membres perdus est en échec. Limites : l'outil compte aussi perdu un membre dont il ne retrouve pas la partition sur un disque présent, ou dont le disque a une taille de bloc différente de celle du volume (sans disque de remplacement trouvé) ; l'exporter, qui ne lit ni les partitions ni la taille de bloc du volume, ne voit pas ces cas, qu'une carte ne produit pas en fonctionnement normal.
- **Assemblage** : l'**état le plus grave** de ses conteneurs membres, dans l'ordre : en échec, dégradé, dégradé en reconstruction, redondance réduite, redondance réduite en reconstruction, optimal. C'est un écart prudent avec l'outil, qui fusionne par rang croissant (section 6 bis) : un membre rencontré ensuite peut y masquer une perte, par exemple « en reconstruction » après « en échec », ou après « dégradé ». Un membre d'état indéterminé rend l'assemblage **indéterminé**, sauf si l'état des autres membres exige déjà une action, selon la même règle que pour un volume de disques : **en échec** si un membre l'est, **dégradé** si l'état le plus grave est dégradé ou dégradé en reconstruction. Un membre disque, absent du relevé, en double, lui-même, ou qui ne désigne pas l'assemblage comme parent, compte comme un membre d'état indéterminé (même règle). Un conteneur pris dans une boucle d'assemblages est **indéterminé** (repérée avant le calcul en suivant les seuls liens d'un assemblage vers ses membres conteneurs, quel que soit l'ordre du relevé). Un assemblage hérite ainsi `reduced_redundancy` d'un RAID 6 membre, sans que lui-même tolère forcément une panne de plus.
- Tout autre niveau, un conteneur en migration ou un conteneur de numérotation classique (dont les règles d'état ne sont pas établies, section 6 bis) : **indéterminé**.

Toutes les lectures du cycle (liste des conteneurs, informations, liste des disques, listes des membres) doivent aboutir, chaque liste des membres doit compter le nombre de membres annoncé, ne lister aucun membre deux fois et donner à chacun un rang distinct inférieur à ce nombre ; sinon la lecture de la configuration de la carte échoue pour le cycle : ni volumes, ni états des disques, ni inventaire et emplacement des disques ne sont publiés.

### État d'un disque

L'exporter publie l'état de chaque disque présent selon les règles de l'outil de référence (section 6 bis, « État d'un disque »), appliquées dans le même ordre à partir de l'information du disque (248), des capacités de la carte (`SupportedOptions2` et `FeatureBits` de la 706, `SupportedOptions` de la 703) et des membres qui le désignent. Un disque désigné par un membre mais absent de la liste des disques est **en panne**. Le libellé de l'outil est publié dans `vendor_state`.

Les états d'un disque ne dépendent pas des drapeaux de partition de ses membres, sauf « en reconstruction » : un disque présent dont la partition est marquée défaillante garde l'état du disque lui-même (par exemple « en ligne ») ; la perte est portée par l'état du volume. Si la 703 n'a pas été lue, la règle du mode HBA natif n'est pas appliquée. Les secours dédiés ne sont pas lus. Écart prudent : un disque à la fois déclaré en panne et « non pris en charge » est publié **en panne** (l'outil peut l'afficher « Unsupported », voire « Hot Spare » ou « Rebuilding » selon les règles suivantes, ce qui le sortirait de toute alerte de panne).

Comme l'outil, l'exporter ne traite comme **disque** qu'un périphérique de type 0 (5 bits bas de l'octet 80 de la 248) dont le bit `0x80` de l'octet 81 est nul [probable : type de périphérique et bit « amovible » de la réponse SCSI `INQUIRY`]. La liste des disques présents peut contenir d'autres périphériques, par exemple le fond de panier d'une baie (type `0x0d`) : ils ne sont pas publiés (ni état de disque, ni inventaire, ni emplacement, ni périphérique Linux). Un membre de volume qui en désigne un est perdu, et le périphérique est publié **en panne**, comme s'il portait le bit de secours : l'outil affiche à sa place un disque « manquant » en panne, l'effet est le même.

Une réponse 248 qui ne contient pas les octets 332-335 ne permet pas d'appliquer la règle « non pris en charge » : l'exporter applique alors, pour le cycle, le repli décrit ci-dessous plutôt que de publier « prêt ».

Si l'information des disques n'a pas pu être lue entièrement pendant le cycle, seuls les disques membres ont un état, déduit des membres (et tout disque qu'une réponse lue déclare en panne, membre ou non) : **en ligne** (membre sans drapeau), **en reconstruction** (bit `0x2`), **en panne** (disque absent ; disque membre que l'information déclare en panne ou qui n'est pas un disque ; et, si l'information a été lue mais sans ses octets 332-335, tout disque qu'elle déclare en panne, membre ou non) ; un disque dont le membre porte le bit `0x4` ou un drapeau inconnu n'est alors pas publié. `vendor_state` est vide.

### État du module de protection du cache (ZMM) [interopérabilité]

Deux lectures `ContainerCommand` dont les données suivent `struct aac_vmioctl` (FreeBSD `aacraid_reg.h`, public) : `Command` = `VM_Ioctl` (3), `ObjType` = `FT_DRIVE` (9), `MethId` = 3, `ObjId` = 0, puis `IoctlCmd`. En-tête : `XferState` = `HostOwned | ApiFib` (`0x00100001`, Linux `aacraid.h`), `StructType` = 1, `SenderSize` = 512. La même enveloppe porte aussi des actions (par exemple `ResetBus`) : la liste blanche n'accepte que ces deux requêtes, octet pour octet.

1. **Présence** : `IoctlCmd` = `0x30000`, 44 octets de données (mots 5 à 10 nuls). Dans la réponse, le mot de 16 bits à l'octet 42 porte le bit `0x2000` quand la carte décrit son module dans la lecture suivante.
2. **État** : `IoctlCmd` = `0x700000`, 76 octets de données (mots 5 à 18 nuls). Dans la réponse, l'octet 20 doit avoir son bit `0x1` (bloc valide) ; le mot d'état R est aux octets 48-51.

Chaque réponse est vérifiée avant décodage : statut (mot 0) nul, écho de `ObjType`, `MethId` et `IoctlCmd`, taille couvrant les champs lus.

Règle appliquée à R, dans cet ordre :

| Condition | État |
|---|---|
| `R & 0xF0000000` différent de `0x40000000` | autre type de module : non décodé |
| aucun bit d'état (`R & 0x0FFFFFFF` nul) | module non installé |
| bit `0x1` absent, d'autres bits présents | indéterminé (`unknown`) ; la présence du module n'est pas publiée |
| bit `0x8` absent | **en panne** (même si le bit `0x4` est présent) |
| un bit autre que `0x1`, `0x4` et `0x8` présent | indéterminé (`unknown`) |
| bit `0x4` présent | en charge |
| sinon | optimal |

Cette règle est celle de l'outil de référence (état de la batterie construit à partir de R, masques `0x1`, `0x4` et `0x8`). Sur l'hôte de référence : R = `0x40000081`, module en panne, conforme à l'affichage de l'outil témoin. Les états « en charge » et « optimal » restent à observer sur matériel. Les conditions sont appliquées dans l'ordre du tableau : un module sans le bit `0x8` est en panne quels que soient ses autres bits (c'est le cas de `0x40000081`) ; un module prêt (`0x8`) portant un bit autre que `0x1`, `0x4` et `0x8`, comme `0x80`, est indéterminé plutôt qu'optimal ou en charge.

## 6 bis. Règles de l'outil de référence pour tous les niveaux [interopérabilité]

Cette section décrit les règles de l'outil de référence (versions 2.05 et 3.07) au-delà des types observés sur l'hôte de référence. **L'exporter applique ces règles avec les écarts indiqués en section 6** (état des volumes, numérotation ARCIO seulement) ; pour le niveau RAID, il suit la table ci-dessous (section 5). Il publie l'état de tous les disques présents selon ces règles (section 6). Ses lectures au-delà de la 706 restent réservées aux couples modèle et firmware qualifiés (section 8).

### Type d'un conteneur et niveau RAID

Sur le chemin de lecture décrit dans ce document (lectures 229, 234, 247, 248 et 249), le type (octets 48-51 de la lecture 234) se lit dans l'une de deux numérotations, choisie **par conteneur** :

- **numérotation ARCIO**, la seule observée sur l'hôte de référence ;
- **numérotation classique**, celle des types `CT_*` publics (FreeBSD `aacraid_reg.h`), quand les octets 100-103 valent 1 ou les octets 96-99 valent 3. Le sens de ces deux champs n'est pas établi ; ils valent 0 pour les cinq conteneurs de l'hôte de référence.

Les deux numérotations n'attribuent pas la même valeur aux mêmes niveaux : 2 et 3 y sont inversés (miroir et agrégat par bandes). Décoder un type sans tenir compte de la numérotation pourrait faire publier un RAID 0 comme un RAID 1.

Conversion de base, hors traitements particuliers (énumérés après le tableau) :

| Type | Numérotation ARCIO | Numérotation classique |
|---|---|---|
| 0 | volume simple (moins de deux membres) ou étendu (au moins deux) | inconnu |
| 1 | volume simple ou étendu (même règle) | volume simple ou étendu (même règle) |
| 2 | RAID 0 | RAID 1 |
| 3 | RAID 1 | RAID 0 |
| 4 | RAID 10 | RAID 5 |
| 5 | RAID 1E | inconnu |
| 6 | inconnu | inconnu |
| 7 | RAID 5 | inconnu |
| 8 | RAID 50 | volume simple ou étendu (même règle) |
| 9 | RAID 5EE | inconnu (affiché « Unknown ») |
| 10 | RAID 5EE | RAID 10 |
| 11 | RAID 6 | RAID 0 |
| 12 | RAID 60 | assemblage (« RAID_volume ») |
| 13 | RAID 6 Reed-Solomon | inconnu (affiché « Unknown ») |
| 14 | RAID 60 Reed-Solomon | RAID 50 |
| 15 | inconnu | RAID 5EE |
| 16 | inconnu | RAID 5EE |
| 17 | inconnu | RAID 1E |
| 18 | inconnu | RAID 6 (Reed-Solomon si le bit `0x4` de l'octet 89 est positionné) |
| 19 | inconnu | RAID 60 (même condition) |
| 20 et au-delà | inconnu | inconnu |

Traitements particuliers : pour le type classique 1, l'outil envoie une commande supplémentaire dont le résultat choisit la façon d'évaluer l'état, sans changer le niveau ; les types classiques 5 et 6 reprennent le niveau d'un autre conteneur quand l'outil le trouve ; le type classique 7, et un conteneur dont les octets 120-123 valent 3, suivent le traitement de migration, où le niveau peut être celui du conteneur d'origine ou de destination. Dans la numérotation ARCIO seulement, un conteneur dont les octets 52-55 valent `0xffffffff` est aussi traité ainsi si un autre conteneur, dont les octets 120-123 valent 3, porte son identifiant aux octets 224-227 ou 228-231.

Un volume (types 0 ou 1 de la numérotation ARCIO) dont un membre est un conteneur présent dans la liste des conteneurs est un **assemblage** : l'outil l'affiche « RAID_volume », quel que soit le type de ses membres (cas de l'hôte de référence, quatre miroirs assemblés). La version 3.07 affiche « 6 » au lieu de « 6 Reed-Solomon » (type ARCIO 13 et type classique 18 avec le bit Reed-Solomon) ; le niveau est le même.

Les règles d'état qui suivent sont celles de la numérotation ARCIO. Pour la numérotation classique, l'outil emprunte d'autres chemins (les RAID 10 et 50 y sont évalués par leurs conteneurs membres), non décrits ici.

### Membres morts et en reconstruction

Pour chaque membre disque, l'outil retient, d'après les drapeaux de la liste des membres (octets 12-15) : bit `0x2` → partition en reconstruction ; sinon bit `0x4` → partition morte ; sinon → normale. Il ignore les autres bits (l'exporter les traite comme indéterminés, section 6). Le membre est **mort** si sa partition est morte, si l'état courant de son disque est « en panne » (cet état prime sur le drapeau de reconstruction), ou si la recherche de sa partition sur le disque n'aboutit pas. Si l'outil ne trouve pas de disque utilisable pour le membre (disque absent, ou dont la taille de bloc diffère de celle du volume et qu'une seconde recherche ne remplace pas), il crée un disque « en panne » qui compte comme mort ; s'il n'y parvient pas, le membre est ignoré. Ces cas ne sont pas observés sur matériel.

Les membres sont parcourus **par rang croissant** (octets 10-11 de l'entrée), qui n'est pas l'ordre de la liste : sur l'hôte de référence, les membres de l'assemblage arrivent dans l'ordre des rangs 0, 3, 1, 2. Le compte k des membres morts augmente de un par membre mort, et de un par membre en reconstruction dans deux cas : le volume est à double redondance (RAID 6, 60), ou le compte vaut encore 0 quand ce membre est parcouru. **Le résultat dépend donc de l'ordre** : en simple redondance, un membre en reconstruction de rang inférieur à celui d'un membre mort donne k = 2 ; l'ordre inverse donne k = 1 (hors inhibition ci-dessous, et si le disque du membre en reconstruction n'est pas en panne). Un membre en reconstruction (son disque reste « en reconstruction ») n'est jamais compté quand les octets 128-131 de la lecture 234 sont non nuls et que les octets 120-123 valent 7 ou 8.

### État d'un volume à membres disques

L'état part d'« optimal » ; avec k = 0, il reste inchangé. Les autres résultats de la conversion ARCIO (niveau inconnu notamment) sont « en échec » dès que k ≥ 1. La variante « en reconstruction » suit la règle décrite après le tableau.

| Famille de niveaux | k = 1 | k = 2 | k ≥ 3 |
|---|---|---|---|
| Simple redondance : 1, 1E, 5, 5EE, 10, 50 | dégradé, ou dégradé en reconstruction | en échec | en échec |
| Double redondance : 6, 60, y compris Reed-Solomon | redondance réduite (encore tolérant à une panne), ou redondance réduite en reconstruction | dégradé, ou dégradé en reconstruction | en échec |
| Sans redondance : 0, volumes simples ou étendus | en échec | en échec | en échec |

Ce tableau donne le résultat du compte k. Pour un conteneur en migration, le niveau qui décide du comptage d'un membre en reconstruction peut être celui du conteneur associé ; l'outil peut ensuite combiner l'état avec un état de reconfiguration, ou le poser « en échec » si le conteneur associé est absent ou en échec.

**Variante « en reconstruction ».** Pour k = 2 en double redondance, il suffit qu'un disque du volume soit en reconstruction. Pour k = 1, l'outil parcourt les disques du volume : un disque en reconstruction pose la variante, un disque en panne rencontré ensuite l'efface. En simple redondance avec k = 1, la version 3.07 ajoute un second parcours : si une partition du volume marquée morte se trouve sur un disque en reconstruction, la variante est effacée. Cas visé [probable] : un disque de remplacement reconstruit plusieurs volumes l'un après l'autre ; un volume dont la partition sur ce disque est encore morte attend son tour et reste « dégradé ».

**L'outil ne tient pas compte des grappes.** Dans ce calcul, qui compte directement les disques, un RAID 10 ou un RAID 50 à deux membres morts est « en échec », même si les deux pannes touchent deux grappes différentes, cas où les données devraient rester lisibles.

### État d'un assemblage

Pour un assemblage dont tous les membres sont des conteneurs, l'état part d'« optimal », puis l'outil y fusionne l'état de chacun de ses conteneurs membres, par rang croissant (un assemblage qui compte aussi des membres disques suit le calcul par disques ci-dessus). Tableau limité aux états de cette section ; la fusion connaît aussi des états de reconfiguration, de système et de vidage du cache, qui produisent des états combinés :

| État du membre fusionné | Effet sur l'état de l'assemblage |
|---|---|
| en échec | en échec |
| dégradé en reconstruction, redondance réduite en reconstruction | remplacé par cet état, quel qu'il soit jusque-là, même « en échec » |
| dégradé | devient « dégradé », sauf s'il est « en échec » |
| redondance réduite | devient « redondance réduite » seulement s'il est « optimal » |
| optimal | inchangé |

Le résultat dépend lui aussi de l'ordre : un membre « en échec » suivi (par rang) d'un membre « dégradé en reconstruction » donne « dégradé en reconstruction ». Sur l'hôte de référence, les quatre miroirs sont optimaux et l'assemblage aussi.

### Conteneurs internes

L'outil n'affiche pas comme volumes autonomes les conteneurs dont le numéro dépasse un seuil propre à la carte (un numéro égal au seuil passe ce filtre ; un autre seuil s'applique si la carte annonce le bit 28 de `SupportedOptions2` et fonctionne en mode « Auto Volume » ou « Simple Volume » ; interpréter ce seuil comme le nombre maximal de volumes est probable), sauf si le bit 22 des octets 88-91 de leur lecture 234 est positionné. Franchir ce seuil ne suffit pas : un conteneur dont la lecture 234 porte le type 15 (numérotation ARCIO) ou 20 (numérotation classique) n'est jamais affiché comme volume autonome, sauf si le traitement de migration change son type. Aucun conteneur de ces types n'est observé sur l'hôte de référence. Ce sont notamment les conteneurs membres d'un assemblage : sur l'hôte de référence, les quatre miroirs `0xfc` à `0xff`, dont la lecture 234 désigne le volume `Array0` comme parent. L'exporter publie leur état.

### État d'un disque

Positions relatives à la lecture 248 ; le tableau décrit le chemin de lecture par la 248 (certaines cartes, selon une capacité annoncée, sautent les lignes 1 à 6). L'outil part de « prêt » (disque disponible), puis applique ces règles **dans cet ordre**, chacune pouvant remplacer l'état posé par les précédentes. « Non en panne » désigne, aux lignes 3 à 6, l'absence du bit `0x1` de l'octet 68 ; aux lignes 7 à 10, l'état courant du disque, qui peut déjà avoir été remplacé par « non pris en charge » :

| Ordre | État | Condition |
|---|---|---|
| 1 | en panne | bit `0x1` de l'octet 68 |
| 2 | non pris en charge | bit `0x2` des octets 332-335 |
| 3 | brut | disque présenté directement au système (ci-dessous), non en panne, dont l'octet 68 n'a pas à la fois les bits `0x10` et `0x40` |
| 4 | en ligne | disque présenté directement au système, non en panne, dont l'octet 68 a les bits `0x10` et `0x40` |
| 5 | brut, ou brut HBA | carte annonçant l'option HBA natif (bit 25 de `SupportedOptions`, commande 703 ; le mode de fonctionnement courant n'est pas testé), disque non en panne et sans le bit `0x40` de l'octet 68 : « brut HBA » si le bit `0x80` de l'octet 69 et le bit `0x1` de l'octet 70 sont positionnés, sinon « brut » |
| 6 | copie de retour | disque non en panne, bit `0x1` de l'octet 0 et bit `0x8` de l'octet 69 |
| 7 | secours | bit `0x4` de l'octet 68 (secours global), disque non en panne. L'outil lit les secours dédiés par une autre commande, que l'exporter n'utilise pas |
| 8 | en ligne | disque membre d'un volume, s'il est encore « prêt » (lignes 8 et 9 appliquées membre par membre, volume après volume : un disque peut déjà être « en reconstruction » quand un autre volume est traité) |
| 9 | en reconstruction | disque portant une partition reconnue en reconstruction (membre de la liste dont le drapeau `0x2` est positionné, associé à ce disque), s'il n'est pas en panne |
| 10 | secours | secours dédié à un volume (autre commande, non utilisée par l'exporter), si le disque n'est ni en panne, ni en reconstruction, ni partitionné |
| — | en panne | disque désigné par un membre ou par la liste des secours globaux mais introuvable, ou qui n'est pas un disque (octets 80-81) ; avec le basculement automatique désactivé, l'outil crée aussi un disque « en panne » pour un membre dont la partition est classée morte (bit `0x4` sans le bit `0x2`), même si le disque est présent |

Un disque est dit ici **présenté directement au système** (interprétation probable de l'effet observé dans l'outil) si la carte annonce l'une de deux capacités dans sa réponse 706 : le bit 16 de `SupportedOptions2` (octets 144-147), pour un disque sans le bit `0x1` de l'octet 68 et auquel manque au moins l'un des bits `0x10` et `0x40` ; ou le bit 27 de `FeatureBits` (octets 36-39, `AAC_FEATURE_JBOD` dans Linux `aacraid.h`), pour un disque dont les bits `0xe0` de l'octet 80 sont nuls. Seule la seconde condition peut mener à la ligne 4. Ce nom traduit l'effet sur l'état affiché [probable] ; la présentation effective du disque au système d'exploitation n'est pas vérifiée. Sur l'hôte de référence, le bit 16 de `SupportedOptions2` et le bit 25 de `SupportedOptions` sont nuls, le bit 27 de `FeatureBits` est positionné (`FeatureBits` = `0x0fff57df`) et l'octet 80 des huit disques est nul : ils remplissent la seconde condition, et la règle de la ligne 4 donne « en ligne ».

Sur l'hôte de référence, l'octet 68 vaut `0x70`, l'octet 69 `0x10`, l'octet 70 et les octets 332-335 sont nuls pour les huit disques, membres des miroirs : « en ligne », conforme à l'affichage de l'outil témoin. Les noms des bits des octets 69, 70 et 332-335 ne sont pas établis ; seul leur effet sur l'état affiché l'est. Les autres états ne sont pas observés sur matériel.

### Octets 120-123 de la lecture 234

La valeur 3 mène au traitement d'une migration ; les valeurs 7 et 8, avec les octets 128-131 non nuls, empêchent de compter un membre en reconstruction (voir plus haut). Le sens général du champ (probablement la tâche en cours sur le volume) et le nom de chaque valeur ne sont pas établis. Le champ vaut 0 pour les cinq conteneurs de l'hôte de référence.

## 7. Domaine de validité [interopérabilité]

Les lectures des sections 4 à 6 forment une interface que le firmware annonce dans la réponse à `RequestSupplementAdapterInfo` (706). Champs publics (`struct aac_supplement_adapter_info`, Linux `aacraid.h` ; FreeBSD `aacraid_reg.h`) :

| Octets | Champ |
|---|---|
| 32-35 | `Version` : version de la zone d'extension |
| 144-147 | `SupportedOptions2` |

Une carte sous `aacraid` annonce ces lectures si `Version` est supérieur à 1 et si le bit `0x08` de `SupportedOptions2` est positionné (`AAC_SUPPORTED_ARCIO_PHYDEV` dans FreeBSD). L'exporter exige les deux conditions, sur une réponse 706 valide (commande en écho, taille couvrant ces champs). Une réponse absente ou invalide est un échec de lecture (`hwraid_collection_success{collector="capabilities"} 0`), pas une absence de prise en charge. Si le bit est absent, la carte est publiée comme non prise en charge pour l'état des volumes et des disques, leur inventaire et leur emplacement, et aucune de ces lectures n'est tentée. La protection du cache, qui ne relève pas de cette interface, reste lue sur une carte qualifiée (section 8).

Sur l'hôte de référence : `Version` = 15, `SupportedOptions2` = `0x00c8693c` (bit `0x08` positionné).

Ce critère, les formats de la section 5 et les règles de la section 6 bis sont les mêmes dans les versions 2.05 (2017) et 3.07 (2023) de l'outil constructeur, aux exceptions signalées en section 6 bis (seconde passe sur la reconstruction, libellés) ; la section 6 décrit le calcul de l'exporter, qui en diffère dans les cas qu'elle indique ; à notre connaissance, la 3.07 est la dernière version publiée de l'outil qui gère les cartes `aacraid`.

Dans les chemins de lecture des volumes et des disques examinés, aucun test du modèle, des identifiants PCI ou de la version du firmware n'a été relevé dans l'outil de référence (à l'ouverture, il lit les identifiants PCI et les relit s'ils sont invalides). Ses bifurcations portent sur la version de la réponse 706 et les capacités annoncées par la carte (`SupportedOptions2` et `FeatureBits` de la 706, `SupportedOptions` de la 703), sur le mode de fonctionnement de la carte (« Auto Volume »), sur son réglage de basculement automatique, et sur des champs propres à chaque conteneur ou disque (section 6 bis). Qu'une carte qui annonce les mêmes capacités, dans le même mode et avec le même réglage, soit lue selon les mêmes règles reste une hypothèse ; seule la qualification (section 8) l'établit pour un couple modèle et firmware.

Ce critère dit quelles cartes annoncent cette interface. Il ne prouve pas qu'un firmware non testé y répond à l'identique : la qualification de la section 8 reste la preuve pour chaque nouveau couple modèle et firmware, et l'exporter publie la version de firmware avec ses métriques.

## 8. Qualification

Chaque lecture utilisée par l'exporter est qualifiée sur le matériel de référence selon l'exigence S4 du cahier des charges :

1. capture des FIB émises par l'outil témoin lors d'une consultation, au moyen de la bibliothèque [`fibcap`](../tools/fibcap), chargée avec `LD_PRELOAD`, qui journalise les ioctl de gestion sans les modifier ([guide de capture](guide-capture.md), [annexe aacraid](guide-capture-aacraid.md)) ;
2. association de chaque FIB à une ligne de l'affichage de l'outil, et comparaison des champs décodés aux valeurs affichées ;
3. pour chaque requête émise par l'exporter : partie significative identique à la capture, autres octets à zéro lorsqu'ils sont démontrés résiduels.

Les captures contiennent des numéros de série et des WWN : elles ne sont pas publiées.
