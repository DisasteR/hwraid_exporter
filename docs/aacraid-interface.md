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
| Informations de la carte sans commande firmware | `FSACTL_GET_HBA_INFO` = `0x42198`, réponse de 200 octets construite par le pilote | `commctrl.c` (`aac_get_hba_info`), `aacraid.h` (`struct aac_hba_info`) |
| Droits | `CAP_SYS_RAWIO` à chaque ioctl | `linit.c` (`aac_cfg_ioctl`) |
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

Le nom, la capacité, le type et l'identifiant unique d'un conteneur sont donc disponibles par la voie publique. L'exporter la privilégie pour ces informations.

## 4. Transfert paginé [observé]

Les informations détaillées sont renvoyées par un transfert en plusieurs FIB, toutes de type `ContainerCommand` / `VM_ContainerConfig`. En-tête de chaque requête : `XferState` = `HostOwned | ApiFib` (`0x00100001`), `StructType` = 1, `Size` = 480, `SenderSize` = 512. Données (huit mots, reste nul) :

| Étape | Mots 0 à 7 de la requête | Réponse |
|---|---|---|
| Ouverture | `2`, sous-commande, paramètre, `0`, marque, `0xffffffff`, `0`, taille | statut, écho, `0xc9`, taille totale, `0xda`, identifiant du transfert |
| Morceau n | `2`, `83`, `0xc9` (n = 0) ou `0xc8`, taille totale, `0xda`, identifiant, n, taille totale | les huit mots, puis **448 octets de données** |
| Clôture | `2`, `84`, `0xc8`, taille totale, `0xda`, identifiant, dernier n lu, taille totale | `0xc6` au mot 2 |

| Lecture | Sous-commande | Paramètre | Marque | Taille |
|---|---|---|---|---|
| Liste des conteneurs | 229 | 0 | `0xda` | 1 032 |
| Information de conteneur | 234 | numéro du conteneur | `0xda` | 4 096 |
| Liste des disques présents | 247 | 0 | 0 | 4 100 |
| Liste des membres | 249 | UID du conteneur | 0 | 8 + 32 × nombre de membres |

L'outil de référence laisse au-delà des huit mots des résidus de ses propres tampons ; ils ne font pas partie de la requête et l'exporter envoie des zéros. Le champ `Size` des réponses de morceau vaut 496 ; le pas de 448 octets est établi par la reconstitution des transferts.

L'exporter ne lit que les morceaux nécessaires aux champs qu'il décode, puis clôture le transfert : sur l'hôte de référence, un seul morceau suffit pour chaque lecture. La clôture après le morceau 0 est une forme employée par l'outil de référence.

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
| 48-51 | type : 1 = assemblage de conteneurs, 3 = miroir de deux disques (seuls types observés) | 1 (volume), 3 (miroirs) |
| 52-55 | UID du conteneur qui l'assemble, `0xffffffff` sinon | `0xffffffff` (volume), UID du volume (miroirs) |
| 56-59 | nombre de membres | 4 (volume), 2 (miroirs) |
| 64-71 | taille en blocs de 512 octets | 62 495 109 120 (= 30 515 190 Mio) |
| 296-311 | nom, chaîne terminée par un octet nul, lue sur 16 octets au plus (longueur maximale non établie) | `Array0` |

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

| Octets | Champ |
|---|---|
| 8-11 | numéro du disque |
| 40-47 | capacité brute en blocs |
| 48-55 | taille utilisable en blocs |
| 88-… | fabricant, modèle, version de firmware, numéro de série (texte) |
| 288-295 | WWN |

### Statistiques du contrôleur (sous-commande 244, paramètre : 0) [observé]

Transfert paginé (section 4) de 2 788 octets sur l'hôte de référence, quand la collecte des statistiques est activée sur la carte. Compteurs cumulés depuis la dernière remise à zéro, entiers de 8 octets :

| Octets | Champ |
|---|---|
| 8-11 | secondes écoulées depuis la dernière remise à zéro |
| 16-103 | commandes en attente : 11 classes (0, 1, 2, 4 … 512) |
| 1136 | lectures de l'hôte (volume 0) |
| 1144 | écritures de l'hôte |
| 1152 | secteurs lus |
| 1160 | secteurs écrits |
| 1184, 1192 | lectures et écritures hors cache |
| 1200 | lectures servies entièrement par le cache DRAM |
| 1208 | lectures en attente d'un chargement ou d'un vidage |
| 1216 | lectures servies en partie par le cache |
| 1224 | écritures absorbées par le cache DRAM |
| 1232 | écritures en attente d'un chargement ou d'un vidage |
| 1240 | vidages du cache DRAM |

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
| bit `0x1` absent | module non installé |
| bit `0x8` absent | **en panne** (même si le bit `0x4` est présent) |
| bit `0x4` présent | en charge |
| sinon | optimal |

Sur l'hôte de référence : R = `0x40000081`, module en panne, conforme à l'affichage de l'outil témoin. Les états « en charge » et « optimal » restent à observer sur matériel.

## 7. Domaine de validité [interopérabilité]

Les lectures des sections 4 à 6 forment une interface que le firmware annonce dans la réponse à `RequestSupplementAdapterInfo` (706). Champs publics (`struct aac_supplement_adapter_info`, Linux `aacraid.h` ; FreeBSD `aacraid_reg.h`) :

| Octets | Champ |
|---|---|
| 32-35 | `Version` : version de la zone d'extension |
| 144-147 | `SupportedOptions2` |

Une carte sous `aacraid` annonce ces lectures si `Version` est supérieur à 1 et si le bit `0x08` de `SupportedOptions2` est positionné (`AAC_SUPPORTED_ARCIO_PHYDEV` dans FreeBSD). L'exporter exige les deux conditions, sur une réponse 706 valide (commande en écho, taille couvrant ces champs). Une réponse absente ou invalide est un échec de lecture (`hwraid_collection_success{collector="capabilities"} 0`), pas une absence de prise en charge. Si le bit est absent, la carte est publiée comme non prise en charge pour l'état des volumes et des disques, et aucune autre lecture d'état n'est tentée.

Sur l'hôte de référence : `Version` = 15, `SupportedOptions2` = `0x00c8693c` (bit `0x08` positionné).

Ce critère, les formats de la section 5 et les règles de la section 6 sont les mêmes dans les versions 2.05 (2017) et 3.07 (2023) de l'outil constructeur ; la 3.07 est la dernière version de l'outil qui gère les cartes `aacraid`.

Ce critère dit quelles cartes annoncent cette interface. Il ne prouve pas qu'un firmware non testé y répond à l'identique : la qualification de la section 8 reste la preuve pour chaque nouveau couple modèle et firmware, et l'exporter publie la version de firmware avec ses métriques.

## 8. Qualification

Chaque lecture utilisée par l'exporter est qualifiée sur le matériel de référence selon l'exigence S4 du cahier des charges :

1. capture des FIB émises par l'outil témoin lors d'une consultation, au moyen de la bibliothèque [`fibcap`](../tools/fibcap), chargée avec `LD_PRELOAD`, qui journalise les ioctl de gestion sans les modifier ([guide de capture](guide-capture.md), [annexe aacraid](guide-capture-aacraid.md)) ;
2. association de chaque FIB à une ligne de l'affichage de l'outil, et comparaison des champs décodés aux valeurs affichées ;
3. pour chaque requête émise par l'exporter : partie significative identique à la capture, autres octets à zéro lorsqu'ils sont démontrés résiduels.

Les captures contiennent des numéros de série et des WWN : elles ne sont pas publiées.
