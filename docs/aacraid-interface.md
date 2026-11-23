# Interface de gestion aacraid : ce qu'utilise l'exporter

Ce document décrit l'interface entre l'exporter et une carte Adaptec gérée par le pilote Linux `aacraid` (première cible : Adaptec 6805, firmware 5.3-0[19204]). Il complète la section 3 du cahier des charges.

Chaque élément porte sa provenance :

- **[public]** : documenté dans une source publique, citée ;
- **[observé]** : établi en observant, sur l'hôte de référence, les échanges entre l'outil constructeur et le pilote, puis en les comparant à ce que l'outil affiche. Ces éléments sont valables pour le modèle et le firmware testés. Ils sont requalifiés pour tout autre modèle ou firmware.
- **[interopérabilité]** : établi par l'analyse de l'outil constructeur, faute de source publique, au titre de l'exception d'interopérabilité (directive 2009/24/CE, article 6 ; code de la propriété intellectuelle, article L122-6-1 IV). Seul le résultat nécessaire à l'interopérabilité de l'exporter est décrit ici ; la méthode et les éléments internes de l'outil ne sont pas publiés. Ces éléments sont qualifiés pour le modèle et le firmware de référence.

Tous les entiers sont en petit-boutiste (*little-endian*). Les positions sont en octets, à partir de 0.

## 1. Accès au pilote [public]

| Élément | Valeur | Source |
|---|---|---|
| Nœud caractère | major dynamique « aac » dans `/proc/devices`, minor = `unique_id` de l'hôte SCSI | `drivers/scsi/aacraid/linit.c` (`register_chrdev`, `aac_cfg_open`) |
| Envoi d'une commande firmware | `FSACTL_SENDFIB` = `0x42008` (tampon de 512 octets) ; `FSACTL_SEND_LARGE_FIB` = `0x42168` | `aacraid.h` (`CTL_CODE`), `commctrl.c` (`ioctl_send_fib`) |
| Plage copiée par le pilote | `max(Size + 32, SenderSize)` octets, au plus 2 048, à l'aller et au retour | `commctrl.c` (`ioctl_send_fib`) |
| Informations de la carte sans commande firmware | `FSACTL_GET_HBA_INFO` = `0x42198`, réponse de 200 octets construite par le pilote | `commctrl.c` (`aac_get_hba_info`), `aacraid.h` (`struct aac_hba_info`) |
| Droits | `CAP_SYS_RAWIO` à chaque ioctl | `linit.c` (`aac_cfg_ioctl`) |
| Sérialisation | un mutex par carte pour tous les ioctl | `commctrl.c` (`aac_do_ioctl`) |

## 2. Bloc de commande firmware (FIB) [public]

En-tête de 32 octets (`struct aac_fibhdr`, `aacraid.h`) :

| Octets | Champ |
|---|---|
| 0-3 | `XferState` |
| 4-5 | `Command` |
| 6 | `StructType` |
| 7 | inutilisé |
| 8-9 | `Size` : taille des données qui suivent l'en-tête |
| 10-11 | `SenderSize` |
| 12-31 | champs techniques (adresses, poignée, chaînage) |

Commandes d'en-tête utilisées par l'exporter :

| `Command` | Nom | Source |
|---|---|---|
| 500 | `ContainerCommand` | Linux `aacraid.h`, FreeBSD `aacraid_reg.h` |
| 700 | `AifRequest` | idem |
| 701, 703, 704 | `CheckRevision`, `RequestAdapterInfo`, `IsAdapterPaused` | FreeBSD `aacraid_reg.h` |
| 803 | `AdapterEnvironmentInfo` (capteurs de température) | FreeBSD `aacraid_reg.h` |

Pour `ContainerCommand`, les données commencent par la commande VM (mot 0) et la sous-commande (mot 1). Valeurs publiques utilisées : `VM_ContainerConfig` = 2, `VM_NameServe` = 1, `VM_NameServe64` = 22 ; sous-commandes `CT_GET_CONTAINER_COUNT` = 4, `CT_GET_SCSI_METHOD` = 64, `CT_CID_TO_32BITS_UID` = 165 (Linux `aacraid.h` et `aachba.c`, FreeBSD `aacraid_reg.h`).

Pour `AifRequest`, le mot 0 des données est la sous-commande : `AifReqJobsForCtr` = 101 (tâches d'un conteneur), `AifReqJobsForScsi` = 102 (tâches d'un disque). La même commande porte des sous-commandes d'action (arrêt, suspension, reprise, lancement de tâche) : la liste blanche de l'exporter contrôle la sous-commande, jamais seulement `Command`.

## 3. Volumes, membres et disques : données publiques [public]

Le pilote Linux interroge lui-même chaque conteneur au démarrage (`VM_NameServe`, `VM_NameServe64`, `CT_CID_TO_32BITS_UID` dans `aachba.c`) et publie le résultat dans sysfs :

- `/sys/class/scsi_device/H:0:T:0/device/level` : type du conteneur (table `container_types` de `aachba.c` : `Volume`, `Mirror`, `Stripe`, `RAID5`, `RAID10`, `RAID6`…) ;
- `/sys/class/scsi_host/hostH/` : `model`, `hba_kernel_version`, `serial_number`, `unique_id`.

Le nom, la capacité, le type et l'identifiant unique d'un conteneur sont donc disponibles par la voie publique. L'exporter la privilégie pour ces informations.

## 4. Transfert paginé [observé]

Les informations détaillées (volume, disque, liste des membres) sont renvoyées par un transfert en plusieurs FIB, toutes de type `ContainerCommand` / `VM_ContainerConfig` :

1. **Ouverture** : la sous-commande de lecture (par exemple 234 pour un volume, 248 pour un disque, 249 pour une liste de membres), avec le paramètre au mot 2 (numéro de conteneur, numéro de disque ou UID). La réponse donne : mot 0 statut (0 = succès), mot 1 écho de la sous-commande, mot 2 `0xc9`, mot 3 taille totale du transfert, mot 4 `0xda`, mot 5 identifiant du transfert.
2. **Lecture des morceaux** : sous-commande 83 avec les mots 2 à 6 = `0xc9` (premier morceau) puis `0xc8`, taille totale, `0xda`, identifiant du transfert, numéro du morceau (0, 1…). La réponse reprend ces 8 mots, suivis de **448 octets de données**.
3. **Clôture** : sous-commande 84 avec les paramètres du dernier morceau ; la réponse porte `0xc6` au mot 2.

Exemples sur l'hôte de référence : 4 096 octets (10 morceaux) pour un volume ou un disque, 136 ou 72 octets (1 morceau) pour une liste de membres.

## 5. Formats observés [observé]

### Information de volume (sous-commande 234, paramètre : numéro de conteneur)

| Octets | Champ | Valeur de référence |
|---|---|---|
| 16-19 | identifiant unique | `0x1D000000` (= « Unique Identifier » de l'outil témoin) |
| 64-71 | taille en blocs de 512 octets | 62 495 109 120 (= 30 515 190 Mo) |
| 296-… | nom, chaîne terminée par un octet nul (longueur maximale non établie) | `Array0` |

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

## 6. État des membres, des volumes et des disques [interopérabilité]

### État d'un membre

Les octets 12 à 15 d'une entrée de la liste des membres portent l'état du membre :

| Bit | Sens |
|---|---|
| `0x2` | membre en cours de reconstruction |
| `0x4` | membre défaillant |
| aucun | membre normal |

Le bit `0x2` est examiné en premier. Sur l'hôte de référence, les 16 entrées ont ces octets à 0.

Un membre est aussi **défaillant** lorsque le disque qu'il désigne (octets 0-3) n'apparaît pas dans la liste des disques présents.

### État d'un conteneur

L'état d'un conteneur ne figure pas tel quel dans les réponses : il se calcule à partir de ses membres et de son niveau RAID.

1. On compte les membres défaillants. Un membre en reconstruction ne contribue pas encore à la redondance : il est compté avec eux, selon des conditions précisées dans le code du module.
2. Aucun membre compté : **optimal**.
3. Sinon, selon la tolérance du niveau RAID :
   - tolérance d'un membre (RAID 1, RAID 5, RAID 10 et apparentés) : un membre compté → **dégradé**, ou **dégradé en reconstruction** si un membre se reconstruit ; deux ou plus → **en échec** ;
   - tolérance de deux membres (RAID 6 et apparentés) : un membre compté → **redondance réduite**, ou **redondance réduite en reconstruction** ; deux → **dégradé**, ou **dégradé en reconstruction** ; davantage → **en échec** ;
   - sans tolérance (RAID 0, volume simple) : un membre compté → **en échec**.

La façon dont l'état d'un conteneur membre (par exemple un miroir dégradé) se reporte sur le conteneur qui l'assemble reste à établir.

### État d'un disque

| État | Condition |
|---|---|
| en ligne | disque présent, membre d'un conteneur |
| en reconstruction | disque portant un membre dont le bit `0x2` est positionné |
| en panne | disque désigné par un membre mais absent de la liste des disques |
| disponible | disque présent, ni membre, ni secours |
| secours, copie de retour | condition à établir |

## 7. Qualification

Chaque lecture utilisée par l'exporter est qualifiée sur le matériel de référence selon l'exigence S4 du cahier des charges :

1. capture des FIB émises par l'outil témoin lors d'une consultation, au moyen d'une bibliothèque chargée avec `LD_PRELOAD` qui journalise les ioctl de gestion sans les modifier ;
2. association de chaque FIB à une ligne de l'affichage de l'outil, et comparaison des champs décodés aux valeurs affichées ;
3. pour chaque requête émise par l'exporter : partie significative identique à la capture, autres octets à zéro lorsqu'ils sont démontrés résiduels.

Les captures contiennent des numéros de série et des WWN : elles ne sont pas publiées.
