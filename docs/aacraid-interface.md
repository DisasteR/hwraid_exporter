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

**Clôture confirmée par `0xc5`.** Sur l'hôte de référence, pendant que maxView interrogeait la carte, deux clôtures sur environ 23 cycles (environ 1 400 transferts) ont été confirmées par `0xc5` au lieu de `0xc6`, toujours avec l'identifiant de notre transfert et après des morceaux conformes ; la marque n'apparaît dans aucune capture de l'outil témoin. Les mots de statut des réponses de pagination sont des codes de statut de commande : les sources publiques n'en nomment qu'un, `CT_OK` = 218 (`0xda`, Linux `aacraid.h`, FreeBSD `aacraid_reg.h`) ; 197 (`0xc5`) et 198 (`0xc6`) n'y ont pas de nom. L'outil de référence rend `CT_OK` après une clôture `0xc6` et le code reçu dans les autres cas ; les fonctions qui demandent les lectures 229, 234, 247, 248 et 249 n'utilisent les données que pour `CT_OK` : **une clôture `0xc5` invalide la lecture** [interopérabilité]. L'exporter fait de même : il rejette la lecture, la refait une fois dans le même cycle, compte le rejet (`hwraid_transfer_close_unconfirmed_total`), et signale une erreur si le second essai est aussi rejeté. La cause exacte de `0xc5` n'est pas établie ; un croisement avec les transferts de maxView est probable. Une seconde piste n'est pas écartée : avant la v0.1.2, l'exporter pouvait être interrompu par ses propres signaux pendant une commande, et le pilote renvoie alors la commande à la carte, qui la reçoit deux fois ([défaut du pilote](aacraid-defaut-pilote.md#mécanisme)). Le 2026-11-24, la v0.1.2 n'a observé aucune `0xc5` en 30 cycles, maxView et ARCCONF interrogeant la carte en même temps ; à ce rythme, on en attendait environ une : ce résultat ne départage pas les deux pistes. Si `hwraid_transfer_close_unconfirmed_total` continue d'augmenter avec la v0.1.2, la seconde piste est affaiblie pour l'exporter, mais pas pour les autres outils : ARCCONF ou maxView interrompus peuvent eux aussi faire envoyer une commande deux fois. Seule une capture des commandes de chaque outil au moment d'une `0xc5` départagerait les pistes.

L'exporter ne lit que les morceaux nécessaires aux champs qu'il décode, puis clôture le transfert : sur l'hôte de référence, un seul morceau suffit pour chaque lecture. La clôture après le morceau 0 est une forme employée par l'outil de référence ; le firmware de l'hôte de référence l'accepte aussi pour les lectures de 4 096 octets (vérifié le 2026-11-23).

Le pilote sérialise chaque ioctl, pas un transfert entier : un autre programme peut intercaler ses propres commandes entre deux morceaux. L'exporter vérifie à chaque réponse le statut (mot 0), l'écho de la sous-commande (mot 1), l'identifiant du transfert et le numéro du morceau ; il refuse une taille totale supérieure à 4 100 octets ; au premier écart il clôture le transfert et rejette le relevé entier du cycle : aucune configuration partielle n'est publiée.

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
| 48-51 | type : 1 = assemblage de conteneurs (« RAID_volume » pour l'outil témoin, concaténation de volumes), 3 = miroir de deux disques (RAID 1) ; seuls types observés | 1 (volume), 3 (miroirs) |
| 52-55 | UID du conteneur qui l'assemble, `0xffffffff` sinon | `0xffffffff` (volume), UID du volume (miroirs) |
| 56-59 | nombre de membres | 4 (volume), 2 (miroirs) |
| 64-71 | taille en blocs de 512 octets | 62 495 109 120 (= 30 515 190 Mio) |
| 148-151 | réglage du cache d'écriture : 2 = « Enabled » (toujours actif, `wb` dans ARCCONF), 1 = « On when protected by battery/ZMM » (`wbb`), 0 = « Disabled » (`wt`) [observé] | 1 |
| 156-159 | état effectif du cache d'écriture : 1 = « On », 0 = « Off » [observé] | 0 (module de protection en panne) |
| 296-311 | nom, chaîne terminée par un octet nul, lue sur 16 octets au plus (longueur maximale non établie) | `Array0` |

**Cache d'écriture.** Positions établies en changeant le réglage avec l'outil témoin et en comparant les lectures de chaque état, sur les cinq conteneurs de l'hôte de référence (module de protection en panne) : réglage « Enabled », état « On » ; réglage « On when protected », état « Off » ; réglage « Disabled », état « Off ». Le passage du deuxième au troisième ne change que l'octet 148 : c'est le réglage. L'octet 156 est le seul autre octet qui ait changé : c'est l'état. Le passage de l'état à « On » avec le réglage « On when protected » et un module opérationnel reste à observer. Une autre valeur de réglage est publiée `unknown` ; une autre valeur d'état n'est pas publiée. Les octets 144 et 152 valent 1 dans toutes les captures, quand l'outil affiche le cache de lecture « Enabled » et « On » ; ils ne sont pas utilisés.

L'exporter publie le niveau RAID de `hwraid_volume_info` dans le vocabulaire normalisé ([metrics.md](metrics.md)) et, dans `vendor_level`, dans les termes de l'outil témoin : type 3 → `raid_level="raid1"`, `vendor_level="1"` ; type 1 → `raid_level="concat"`, `vendor_level="RAID_volume"` (assemblage par concaténation) ; tout autre type → `raid_level="unknown"`, `vendor_level="type N"`.

### Liste des membres (sous-commande 249, paramètre : UID du conteneur)

En-tête de 8 octets (mot 1 = nombre de membres), puis une entrée de 32 octets par membre :

| Octets | Champ |
|---|---|
| 0-3 | numéro du disque membre, `0xffffffff` si le membre est un conteneur |
| 4-7 | UID du conteneur membre, `0xffffffff` si le membre est un disque |
| 10 | rang du membre |
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

Si les deux bits sont positionnés, le membre est traité comme en reconstruction. Un autre bit positionné rend l'état du membre **indéterminé**. Sur l'hôte de référence, les 16 entrées ont ces octets à 0.

Un membre est aussi **défaillant** lorsque le disque qu'il désigne (octets 0-3) n'apparaît pas dans la liste des disques présents. Cette règle n'est appliquée que sur une liste des disques lue complètement dans le même cycle ; sinon, l'état est **indéterminé**.

### État d'un conteneur

L'état d'un conteneur ne figure pas tel quel dans les réponses : il se calcule à partir de ses membres et de son type. Seuls les types observés sur l'hôte de référence sont évalués :

- **miroir** (type 3, deux disques) : si un membre a un drapeau inconnu, ou si la liste des membres ne compte pas les deux membres annoncés, l'état est **indéterminé**. Sinon on compte les membres défaillants et en reconstruction : aucun → **optimal** ; un, en reconstruction → **dégradé en reconstruction** ; un, défaillant → **dégradé** ; deux → **en échec** ;
- **assemblage** (type 1) : **optimal** si tous ses conteneurs membres le sont ; sinon **indéterminé**, la façon dont l'état d'un conteneur membre se reporte sur l'assemblage n'étant pas établie. Les miroirs publient leur propre état : une perte de redondance reste visible à leur niveau ;
- tout autre type : **indéterminé**.

Toutes les lectures du cycle (liste des conteneurs, informations, liste des disques, listes des membres) doivent aboutir ; sinon aucun état n'est publié pour ce cycle.

### État d'un disque

| État | Condition |
|---|---|
| en ligne | disque présent, membre d'un conteneur, sans drapeau |
| en reconstruction | disque portant un membre dont le bit `0x2` est positionné |
| en panne | disque désigné par un membre mais absent de la liste des disques présents |
| non publié | disque présent dont le membre porte le bit `0x4` ou un drapeau inconnu ; disque présent qui n'est membre d'aucun conteneur (secours, disponible : condition à établir) |

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

Sur l'hôte de référence : R = `0x40000081`, module en panne, conforme à l'affichage de l'outil témoin. Les états « en charge » et « optimal » restent à observer sur matériel.

## 7. Domaine de validité [interopérabilité]

Les lectures des sections 4 à 6 forment une interface que le firmware annonce dans la réponse à `RequestSupplementAdapterInfo` (706). Champs publics (`struct aac_supplement_adapter_info`, Linux `aacraid.h` ; FreeBSD `aacraid_reg.h`) :

| Octets | Champ |
|---|---|
| 32-35 | `Version` : version de la zone d'extension |
| 144-147 | `SupportedOptions2` |

Une carte sous `aacraid` annonce ces lectures si `Version` est supérieur à 1 et si le bit `0x08` de `SupportedOptions2` est positionné (`AAC_SUPPORTED_ARCIO_PHYDEV` dans FreeBSD). L'exporter exige les deux conditions, sur une réponse 706 valide (commande en écho, taille couvrant ces champs). Une réponse absente ou invalide est un échec de lecture (`hwraid_collection_success{collector="capabilities"} 0`), pas une absence de prise en charge. Si le bit est absent, la carte est publiée comme non prise en charge pour l'état des volumes et des disques, leur inventaire et leur emplacement, et aucune de ces lectures n'est tentée. La protection du cache, qui ne relève pas de cette interface, reste lue sur une carte qualifiée (section 8).

Sur l'hôte de référence : `Version` = 15, `SupportedOptions2` = `0x00c8693c` (bit `0x08` positionné).

Ce critère, les formats de la section 5 et les règles de la section 6 sont les mêmes dans les versions 2.05 (2017) et 3.07 (2023) de l'outil constructeur ; la 3.07 est la dernière version de l'outil qui gère les cartes `aacraid`.

Ce critère dit quelles cartes annoncent cette interface. Il ne prouve pas qu'un firmware non testé y répond à l'identique : la qualification de la section 8 reste la preuve pour chaque nouveau couple modèle et firmware, et l'exporter publie la version de firmware avec ses métriques.

## 8. Qualification

Chaque lecture utilisée par l'exporter est qualifiée sur le matériel de référence selon l'exigence S4 du cahier des charges :

1. capture des FIB émises par l'outil témoin lors d'une consultation, au moyen de la bibliothèque [`fibcap`](../tools/fibcap), chargée avec `LD_PRELOAD`, qui journalise les ioctl de gestion sans les modifier ([guide de capture](guide-capture.md), [annexe aacraid](guide-capture-aacraid.md)) ;
2. association de chaque FIB à une ligne de l'affichage de l'outil, et comparaison des champs décodés aux valeurs affichées ;
3. pour chaque requête émise par l'exporter : partie significative identique à la capture, autres octets à zéro lorsqu'ils sont démontrés résiduels.

Les captures contiennent des numéros de série et des WWN : elles ne sont pas publiées.
