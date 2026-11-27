# Cahier des charges — Exporter Prometheus pour les contrôleurs RAID matériels sous Linux

Version de cadrage 0.11 · 23 novembre 2026  
Première cible de validation : Adaptec RAID 6805 (série 6), Debian 11 / OpenMediaVault, noyau 5.10, Linux x86‑64.

## 1. Objectif et décision d’architecture

Depuis Prometheus, l’administrateur doit pouvoir répondre à quatre questions : quelle grappe a perdu sa redondance, quel membre est concerné, la reconstruction avance-t-elle, et le cache de la carte bénéficie-t-il d’un module de protection opérationnel ?

Livrer un exporter en lecture seule, écrit en Go et distribué sous deux formes : un binaire Linux statique autonome et une image Docker `FROM scratch` contenant ce même binaire, à raison d’une instance par hôte. Il inventorie les contrôleurs de stockage visibles sur l’hôte Linux, identifie ceux capables de gérer du RAID matériel et sélectionne un module de collecte pour chaque carte. Il collecte en arrière-plan dès le démarrage, y compris sans client Prometheus. `/metrics` sert le dernier instantané publié en mémoire.

L’exporter parle directement au pilote du noyau de l’hôte, par les interfaces que celui-ci publie. Il ne dépend à l’exécution d’aucun outil constructeur (ARCCONF, StorLib, maxView, `storcli`, `ssacli`) ni d’un script tiers, et il n’en embarque aucun. Un outil constructeur peut être installé et actif sur le même hôte : l’exporter doit alors cohabiter avec lui (section 6.3). Le conteneur historique `docker-arccheck` sert de témoin pendant la validation. La première carte qualifiée sera la 6805. Toute annonce de compatibilité supplémentaire devra préciser le modèle, le firmware et les fonctions effectivement testées. [Projet historique](https://github.com/DisasteR/docker-arccheck) · [Fiche Adaptec série 6](https://storage.microsemi.com/nr/pdfs/series6_6t_ds.pdf)

Sur la 6805 de référence, chaque disque physique est accessible à `smartctl` via `/dev/sgX`. Pour tout contrôleur offrant un accès direct équivalent, la collecte SMART reste indépendante. Lorsqu’une carte masque ses disques à Linux, le module RAID interroge son interface de gestion pour inventorier les membres et publier leur état vu par le contrôleur, dans la limite des fonctions disponibles. La présence d’un volume logique ou l’absence d’un `/dev/sgX` individuel ne permet pas de conclure sur la santé SMART d’un disque. Utiliser le WWN ou le numéro de série pour corréler les deux inventaires lorsque possible. [smartctl_exporter](https://github.com/prometheus-community/smartctl_exporter)

### 1.1 Couverture et sélection du module

La cible produit inclut tous les contrôleurs RAID matériels de l’hôte, y compris plusieurs constructeurs simultanément. La supervision complète s’acquiert famille par famille. Chaque carte découverte figure dans l’inventaire, même si aucun module qualifié ne sait encore interroger son firmware. Les volumes logiciels (mdadm, ZFS) relèvent d’une autre couche.

| Famille ou situation | Traitement prévu | Référence |
|---|---|---|
| Adaptec série 6 / `aacraid` | Premier module à développer (section 3). | Pilotes Linux et FreeBSD. |
| Broadcom / LSI MegaRAID et variantes OEM utilisant `megaraid_sas` | Module candidat. Qualifier par identifiants matériels, mode et firmware. | Pilote Linux, `mfireg.h` de FreeBSD (commandes DCMD). |
| Smart Array / `hpsa` et Smart Storage / `smartpqi` | Modules candidats distincts. Documenter les fonctions par pilote et par modèle. | Pilotes Linux, interface CISS. |
| Famille inconnue ou accès incomplet | Publier identité, pilote si connu et statut de prise en charge. Une permission manquante sur un module connu est une erreur d’accès. | Inventaire Linux de l’hôte. |

Chaque lecture utilisée par un module est qualifiée par comparaison avec l’outil témoin de sa famille (capture sur le matériel de référence, ou forme dérivée selon S4), et inscrite dans la matrice de qualification du dépôt.

**Base légale des informations non documentées.** Lorsqu’aucune source publique ne décrit une information nécessaire (par exemple l’état des membres d’un volume sur les cartes Adaptec), elle est établie par l’analyse de l’outil constructeur, au titre de l’exception d’interopérabilité (directive 2009/24/CE, article 6 ; code de la propriété intellectuelle, article L122-6-1 IV). Ces informations ne sont communiquées que dans la mesure nécessaire à l’interopérabilité de l’exporter : son code et la description de l’interface qu’il utilise ([docs/aacraid-interface.md](../docs/aacraid-interface.md)). L’analyse elle-même (méthode, éléments internes de l’outil) n’est pas publiée. Les textes applicables (Union européenne, France, États-Unis, Royaume-Uni, Allemagne) et les risques sont détaillés dans [docs/base-legale-interoperabilite.md](../docs/base-legale-interoperabilite.md).

## 2. Exigences fonctionnelles

| Objet | Données attendues | Comportement à garantir |
|---|---|---|
| Contrôleur | Identifiant stable, modèle, firmware, adresse PCI, état général. Température si le matériel la fournit. | Redécouvrir les cartes. Une erreur sur une carte laisse les résultats des autres exploitables. |
| Volume logique | Identifiant local, nom si disponible, niveau RAID, capacité en octets, état optimal/dégradé/en échec ou inconnu. | Identifier le volume par le couple contrôleur + identifiant local. Deux cartes peuvent toutes deux posséder un volume 0. |
| Correspondance Linux (facultative) | Associer un volume logique au périphérique bloc visible sur l’hôte (par exemple `/dev/sda`) lorsque le lien est certain. | Une absence, une ambiguïté ou un renommage du périphérique préserve la collecte RAID et les labels `controller` et `volume`. |
| Disque physique | Identifiant, WWN ou numéro de série si disponible, emplacement exposé, état et rôle. | Représenter les membres actifs, les disques de secours, les disques non affectés et les membres manquants signalés par la carte. |
| Appartenance | Relation explicite entre volume et membre physique. | Conserver les relations multiples si le contrôleur les rapporte. Une baie dont l’occupant a disparu peut être identifiée par son emplacement sans lui inventer de WWN. |
| Tâche de fond | Type, cible et avancement lorsque fourni. Priorité à la reconstruction. | Attribuer la tâche au bon contrôleur et à la bonne cible. Une tâche active à 0 % diffère d’une absence de tâche. Un type de tâche non reconnu est publié comme `other`. |
| Protection du cache | Présence et état du module de protection du cache, par contrôleur. Première cible : AFM‑600 (ZMM) de la 6805. | Distinguer absence confirmée, module présent, état inconnu, lecture impossible et fonction non prise en charge. Un module en charge ne doit pas déclencher une alerte « module absent ». |

L’état du module seul ne suffit pas à conclure que le cache d’écriture est activé, désactivé ou protégé pour chaque volume : le réglage configuré et l’état effectif du cache d’écriture sont publiés par volume (`hwraid_volume_write_cache_policy`, `hwraid_volume_write_cache_enabled`).

## 3. Module AACRAID

Les éléments ci-dessous décrivent l’interface du pilote Linux `aacraid` telle qu’elle figure dans ses sources. Ils ont été vérifiés sur Linux v5.10 (noyau de l’hôte de référence) et v6.12, qui sont équivalents sur tous les points cités. Le noyau Debian applique des correctifs à la version d’origine : la recette sur matériel reste la preuve finale.

### 3.1 Interface du pilote

| Mécanisme | Ce que fait le pilote | Conséquence pour l’exporter |
|---|---|---|
| Nœud caractère de gestion | `register_chrdev(0, "aac", …)` : major attribué dynamiquement, visible dans `/proc/devices`. Le pilote ne crée aucun nœud dans `/dev` (pas de `device_create`), udev non plus. Le major est libéré quand la dernière carte disparaît et réattribué au probe suivant. | Le nœud est créé par l’exporter lui-même au démarrage (section 6.2). Son major peut changer sans redémarrage. |
| Sélection de la carte | `aac_cfg_open` associe le minor du nœud à `aac->id`, égal à `shost->unique_id`. Ce numéro est le premier libre au moment du probe : il dépend de l’ordre de détection et des autres cartes. | Le minor vaut l’attribut sysfs `unique_id` de l’hôte SCSI, et non le H de `hostH`. Il ne sert jamais d’identité durable. |
| Durée de vie du descripteur | `aac_cfg_open` mémorise la carte sans compteur de références. | Le nœud est ouvert puis refermé à chaque cycle. Aucun descripteur n’est conservé entre deux cycles. |
| Commandes au firmware | `FSACTL_SENDFIB` et `FSACTL_SEND_LARGE_FIB` transmettent un bloc FIB (*Firmware Interface Block*) au firmware. `ioctl_send_fib` contrôle les tailles et traite à part `header.Command == TakeABreakPt` (interruption de la carte sans envoi). Aucune autre commande n’est filtrée. | Le pilote ne distingue pas lecture et écriture (section 4). |
| Autres ioctl du même point d’entrée | `aac_dev_ioctl` traite d’abord `FSACTL_QUERY_DISK`, `FSACTL_DELETE_DISK`, `FSACTL_FORCE_DELETE_DISK` et `FSACTL_GET_CONTAINERS` (nouveau balayage firmware). Puis `FSACTL_MINIPORT_REV_CHECK`, `FSACTL_GET_PCI_INFO`, `FSACTL_GET_HBA_INFO`, `FSACTL_SEND_RAW_SRB`, `FSACTL_RESET_IOP` et l’abonnement aux événements (`FSACTL_OPEN_GET_ADAPTER_FIB` et suivants). | Seules les entrées de la liste blanche sont autorisées (section 4). |
| Droits | `CAP_SYS_RAWIO` est exigée à chaque ioctl, par `capable()`, donc dans l’espace de noms utilisateur initial. `FSACTL_SEND_RAW_SRB` et l’écriture de l’attribut `reset_host` exigent en plus `CAP_SYS_ADMIN`. | L’ouverture en lecture seule suffit au transport sans restreindre les commandes. Le processus de collecte ne reçoit jamais `CAP_SYS_ADMIN`. |
| Second chemin d’accès | `aac_ioctl`, déclaré dans le gabarit d’hôte SCSI, mène au même `aac_do_ioctl` après un test de santé de la carte. Il est atteint par tout ioctl inconnu de `sd` sur un disque de la carte (`/dev/sdX`, même ouvert en lecture seule) et de `sg` (`/dev/sgN`, ouvert en lecture-écriture). | Transmettre à un processus un `/dev/sdX` ou un `/dev/sgN` (en `rw`) d’une carte AAC avec `CAP_SYS_RAWIO` lui donne un accès complet au contrôleur. L’exporter n’utilise pas ce chemin. |
| Sérialisation | `aac_do_ioctl` prend un mutex par carte (`ioctl_mutex`, attente non interruptible). | Les commandes de tous les processus (exporter, outil constructeur, `smartctl`) passent une par une. Une commande lente d’un autre outil retarde l’exporter, qui peut rester bloqué sans pouvoir être interrompu. |
| Attente du firmware | `aac_fib_send` attend la réponse de façon interruptible. Interrompue par un signal, elle renvoie `-ERESTARTSYS`, et l’appel peut être relancé automatiquement. | Une même commande peut être envoyée plusieurs fois au firmware (section 4, S8). |

[aacraid.h](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/aacraid/aacraid.h) · [commctrl.c](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/aacraid/commctrl.c) · [linit.c](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/aacraid/linit.c) · [commsup.c](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/aacraid/commsup.c) · [linit.c v5.10](https://github.com/torvalds/linux/blob/v5.10/drivers/scsi/aacraid/linit.c)

### 3.2 Données disponibles sans commande au firmware

Le pilote publie dans sysfs, depuis un cache rempli à l’initialisation de la carte :

- sur l’hôte SCSI (`/sys/class/scsi_host/hostH/`) : `model`, `vendor`, `hba_kernel_version` (firmware), `hba_monitor_version`, `hba_bios_version`, `driver_version`, `serial_number`, et `unique_id` (attribut du cœur SCSI, renseigné par le pilote) ;
- sur chaque périphérique SCSI de la carte : `level` et `unique_id`. Sur le canal des conteneurs, `level` donne le niveau RAID et `unique_id` les 16 octets d’identifiant du conteneur. Ailleurs, `level` vaut `Hidden`, `JBOD` ou vide, et `unique_id` est nul.

`serial_number` est dérivé du mot de série du firmware (24 bits utiles affichés en `%06X`) ou du numéro de série PCBA, et vide si le firmware renvoie la sentinelle `0xBAD0`. L’exporter le traite comme une chaîne opaque soumise au contrôle d’unicité de la section 5. L’inventaire du contrôleur (identité, modèle, firmware) ne nécessite donc aucune commande au firmware.

L’attribut `reset_host` est inscriptible et réinitialise la carte : sysfs reste toujours en lecture seule pour l’exporter.

### 3.3 Lectures à travers le firmware

| Fonction | Base publique | État de l’établissement sur la 6805 (firmware 5.3-0[19204]) |
|---|---|---|
| Liste, noms, taille, type et identifiant des volumes | `VM_NameServe`, `VM_NameServe64`, `CT_CID_TO_32BITS_UID` (165), utilisés par le pilote Linux, dont le résultat est publié dans sysfs (`level`). | Voie publique retenue. Les informations détaillées de volume (transfert paginé, sous-commande 234) sont établies par observation : identifiant, taille et nom concordent avec le témoin. |
| Composition des volumes (membres) | Aucune documentation publique. | Établie par observation : liste des membres (sous-commande 249), avec pour chaque membre le disque ou le conteneur, le rang, le début et la taille. Décomposition de l’hôte de référence (volume de quatre miroirs, huit disques) identique à celle du témoin. |
| État de redondance d’un volume | Aucune commande documentée publiquement ne porte cet état. Le drapeau « lecture seule » d’un conteneur n’est qu’un indice. | Établi et implémenté (collecteurs `volumes` et `disks`) : liste des conteneurs (229), informations (234), disques présents (247) et membres (249) lus à chaque cycle, environ 40 FIB ; état calculé pour les miroirs, assemblage optimal ou indéterminé, autres types indéterminés. Qualifié sur l’état optimal de l’hôte de référence et validé le 2026-11-23 dans le profil de conteneur de la section 6.5 (5 conteneurs, 8 disques en ligne, clôture des transferts après le premier morceau acceptée par le firmware) ; états dégradé, en reconstruction et en échec couverts par des tests sur données construites, à observer sur matériel. |
| Inventaire des disques | Aucune documentation publique complète. | Établi par observation (sous-commande 248) et implémenté (collecteur `disk_inventory`, `hwraid_disk_info`, `hwraid_disk_capacity_bytes`) : fabricant, modèle, firmware, numéro de série, WWN et capacité vérifiés sur les huit disques de l'hôte de référence contre l'outil témoin ; lecture par l'exporter validée sur l'hôte de référence le 2026-11-23 dans le profil de conteneur de la section 6.5 (huit disques inventoriés, collecteur à 1). Emplacement (connecteur, position, phy) lu dans la carte des connecteurs (commande 708, `FSACTL_SEND_LARGE_FIB`, une fois par carte) : `hwraid_disk_location`, collecteur `disk_location` ; position identique à celle de l'outil témoin et vérifiée sur le câblage réel ; lecture par l'exporter validée le 2026-11-23 dans le profil de conteneur de la section 6.5. Lien avec le périphérique Linux (`hwraid_disk_os_device`) lu dans sysfs, contrôlé par le numéro de série de la page VPD 0x80 ; validé le 2026-11-23 sur l'hôte de référence (huit disques, `sg1` à `sg8`). |
| Progression des tâches | Interrogation synchrone publique : `AifRequest` / `AifReqJobsForCtr` (101, par conteneur) et `AifReqJobsForScsi` (102, par disque). FreeBSD documente le contenu des rapports de progression : `aac_AifJobProgressReport` (`status`, `finalTick`, `currentTick`) et l’énumération des types de tâche. | Établi sur l’outil (versions 2.05 et 3.07) : 101 par conteneur, puis `AifReqJobReport` (103) pour la première tâche listée (une tâche affichée par volume) ; blocs de 48 octets à compteurs de 64 bits (`docs/aacraid-interface.md`, section 5). Implémenté ; aucun rapport capturé à ce jour (vérification à la prochaine reconstruction). |
| Statistiques du contrôleur et du cache DRAM | Aucune documentation publique. | Établi par observation (sous-commande 244) : entrées-sorties et secteurs par volume, accès servis par le cache DRAM, vidages, attentes, commandes en attente. Histogrammes de latence et compteurs d'erreurs par disque présents, positions à établir. Lecture de 9 FIB, sans remise à zéro des compteurs. |
| Protection du cache (ZMM) | Enveloppe publique : `struct aac_vmioctl` (FreeBSD) avec `VM_Ioctl`, `FT_DRIVE` ; valeurs d'`IoctlCmd` et format de réponse non documentés. | Établi : deux lectures (présence `0x30000`, état `0x700000`) et règle de l'état (non installé, en panne, en charge, optimal), vérifiés sur le module en panne de l'hôte de référence et implémentés (collecteur `cache_protection`, lu à chaque cycle) ; validés sur l'hôte de référence le 2026-11-23 dans le profil de conteneur de la section 6.5 (`hwraid_cache_protection_state{state="failed"}`). État optimal observé le 2026-11-27 sur le module de remplacement (R = `0x4000008b`, « ZMM Optimal ») ; règle alignée ce jour sur celle de l'outil (bit `0x2` compris), l'état en charge reste à capturer. |

**Domaine de validité.** Ces lectures ne sont employées que si la réponse publique à `RequestSupplementAdapterInfo` (706) annonce `Version` > 1 et le bit `0x08` de `SupportedOptions2` (`AAC_SUPPORTED_ARCIO_PHYDEV`, FreeBSD `aacraid_reg.h`). Le module garde cette réponse tant que la carte (adresse, nœud, identité matérielle : numéro de série, identifiants PCI, modèle, firmware) ne change pas ; il la relit si cette identité change, si un de ses champs est illisible (rien n'est alors gardé) ou si la lecture a échoué. Si le bit est absent, il ne tente aucune lecture de volume, de membre ou de disque et publie ces fonctions non prises en charge ; la protection du cache, qui ne relève pas de cette interface, reste lue sur une carte qualifiée. Le résultat de cette lecture est publié à chaque cycle par `hwraid_collection_success{collector="capabilities"}` : 1 si la réponse est connue, 0 si la lecture vient d’échouer (elle est retentée au cycle suivant). Tant que la réponse est gardée, ce collecteur ne publie pas de date de dernier succès ; relue à chaque cycle sous identité incomplète, elle est datée (`docs/metrics.md`). La 6805 de référence annonce le bit. Vérifié sur l’hôte de référence le 2026-11-23, dans le profil de conteneur de la section 6.5 : la requête de l’exporter, octets 12 à 31 à zéro, est acceptée par le firmware, la réponse est décodée et le bit est lu présent.

Le détail des formats et du protocole paginé figure dans [docs/aacraid-interface.md](../docs/aacraid-interface.md).

[FreeBSD aacreg.h](https://cgit.freebsd.org/src/tree/sys/dev/aac/aacreg.h) · [FreeBSD aacraid_reg.h](https://cgit.freebsd.org/src/tree/sys/dev/aacraid/aacraid_reg.h)

Règles de décodage de la progression : ratio = `currentTick / finalTick`. Si `finalTick` vaut 0 ou si `currentTick` dépasse `finalTick`, le ratio est omis et l’anomalie journalisée. Les types reconstruction (RAID 5, RAID 6, miroir) donnent `task="rebuild"`. Les autres types connus (vérification, mise à zéro, migration, copie de retour, extension) reçoivent un nom dédié, et un type inconnu donne `task="other"`.

Chaque champ décodé documente son origine, sa taille, son ordre des octets et les versions de firmware validées.

## 4. Exigences de sécurité

Le pilote ne distingue pas une lecture d’une écriture : l’ouverture en lecture seule, les permissions du nœud et la capacité `CAP_SYS_RAWIO` ne limitent pas ce que l’exporter peut envoyer au contrôleur. La garantie de lecture seule repose donc sur le code de l’exporter. Elle est formulée comme une suite d’exigences vérifiables.

| Exigence | Critère de vérification |
|---|---|
| **S1. Couche de transport unique par pilote.** Un seul paquet Go par pilote importe `syscall` ou `golang.org/x/sys/unix` et émet des ioctl. | Analyse statique des imports en CI : tout import de `syscall`, `golang.org/x/sys/unix` ou usage de `SyscallConn` hors des paquets de transport fait échouer la construction. |
| **S2. Liste blanche positive et fermée.** Le transport n’émet que des requêtes figurant dans une liste déclarée. Pour `aacraid`, une entrée est la clé (code ioctl, `header.Command`, commande du corps, sous-type, taille de requête min/max). La vérification porte sur les octets finaux sérialisés, juste avant l’appel système. Tout le reste est refusé. N’y figurent jamais : `FSACTL_RESET_IOP`, `FSACTL_SEND_RAW_SRB`, `FSACTL_DELETE_DISK`, `FSACTL_FORCE_DELETE_DISK`, `FSACTL_GET_CONTAINERS`, l’abonnement aux événements, `TakeABreakPt`, ni aucune commande de configuration. | Test à base de propriétés : pour des codes ioctl et des combinaisons de sélecteurs FIB tirés au hasard (dont toutes les valeurs connues des sources), seules les entrées de la liste atteignent le faux appel système. Un second test vérifie que la liste chargée est identique à la liste déclarée. |
| **S3. Ajout tracé.** Toute entrée ajoutée à la liste blanche référence sa ligne de la matrice de qualification. | Revue obligatoire de toute modification du fichier de liste blanche. |
| **S4. Première émission sans risque nouveau.** Une commande qui n’est pas décrite par une source publique n’entre dans la liste blanche que si l’une de deux preuves l’établit. (a) **Capture** : le firmware la reçoit déjà de l’outil témoin lors d’une opération de consultation (`GETCONFIG`, `GETSTATUS`), sur le même modèle (quatre identifiants PCI). (b) **Forme dérivée** : elle découle d’une forme de base prouvée par (a), par une règle de construction de l’outil (versions 2.05 et 3.07) établie par l’analyse de l’outil et confirmée en observant son comportement ; c’est le cas des formes que la carte de référence ne déclenche pas (section 6.11 : capacités différentes, codes de réponse atypiques). La version de firmware des captures est consignée. Lire une autre version du même modèle sans nouvelle capture est une décision de risque, acceptée : les preuves portent sur les requêtes que construit l’outil témoin, non sur les réponses d’un autre firmware ; l’exporter construit ses requêtes à partir des capacités annoncées par la carte, comme l’outil, qui ne teste pas la version de firmware dans ses chemins de lecture (section 6.11). La documentation distingue le modèle reconnu, les versions de firmware effectivement testées et l’autorisation de collecter. La partie significative de la requête doit être identique à celle du témoin : commande, sous-commande, tailles, paramètres et structure de données. Les octets situés hors de cette structure sont mis à zéro, à deux conditions : la structure est délimitée par les captures, et ces octets y sont démontrés résiduels, c’est-à-dire qu’ils varient d’un envoi à l’autre du témoin sans effet sur la réponse. Lorsque le témoin envoie déjà la commande avec ces octets à zéro, c’est cette variante qui est reproduite à l’identique. | La matrice de qualification conserve, pour chaque entrée, les captures du témoin (preuve a) ou la règle de construction, sa forme de base et sa confirmation (preuve b), la délimitation de la partie significative et, le cas échéant, la preuve du caractère résiduel des autres octets. Un test compare la requête construite par l’exporter à la capture (preuve a) ou à la requête de référence que donne la règle confirmée (preuve b) : identité sur la partie significative, zéros ailleurs, avec la justification des octets mis à zéro. La matrice indique pour chaque forme la portée de sa confirmation (requête entière, en-tête seul, situation observée). Un mode `--dry-run`, qui journaliserait les requêtes sans les émettre, est prévu (section 7.7) ; à ce jour, la séquence est vérifiée par les tests sur faux pilote. |
| **S4 bis. Modèle non qualifié (option `-allow-unqualified`, désactivée par défaut).** Sur une carte dont le modèle n’est pas qualifié et que l’opérateur a désignée (section 6.12), l’exigence d’une preuve sur le même modèle est levée : l’exporter y envoie les formes de la section 6.11 selon sa règle de conduite (suite de commandes de l’outil, sans les écarts E1 et E2, avec les écarts E3 et E4), prouvées selon S4 (capture ou forme dérivée) sur le modèle de référence et décrites dans `docs/aacraid-interface.md` [interopérabilité], avec le même critère de recette que S4 (partie significative, octets mis à zéro et leur justification). Le risque restant, accepté par l’opérateur, est qu’un modèle non testé réponde autrement que l’outil ne l’attend. | Tests de la section 6.11 (une forme par combinaison de capacités, non-régression du modèle qualifié) ; une forme dont la preuve S4 est partielle (statut de preuve de la section 6.11) reste refusée, option comprise ; test qu’une carte d’un modèle non qualifié et non désignée ne reçoit aucune commande firmware (`GET_HBA_INFO` seulement, réponse du pilote). La documentation publique signale chaque forme jamais observée sur matériel. |
| **S5. Ouverture minimale.** `aacraid` et `megaraid_sas` : nœud ouvert en `O_RDONLY\|O_NOFOLLOW\|O_CLOEXEC`. `hpsa` et `smartpqi` : le noyau impose `O_RDWR` sur `sg` pour ces ioctl, la sécurité repose alors entièrement sur S2. sysfs est lu en lecture seule. | Test de démarrage avec `/sys` en lecture seule et nœud transmis avec la permission Docker `r` pour `aacraid`. Test qui échoue si le transport `hpsa`/`smartpqi` ouvre en `O_RDONLY` (refus `EPERM` du noyau). |
| **S6. Préparation puis abandon des privilèges.** Seule la phase de préparation, exécutée au démarrage avant toute lecture d’entrée externe et avant l’ouverture du port HTTP, crée ou supprime des nœuds, et uniquement dans `/dev/hwraid_exporter/` (section 6.2). Elle se termine par un abandon irréversible des privilèges : identité non root, capacités réduites à celles des pilotes présents dans tous les ensembles (effectif, permis, hérité, ambiant, limite), `no_new_privs`. Le processus vérifie le résultat dans `/proc/self/status` et s’arrête immédiatement si un écart subsiste. Le binaire n’est jamais installé avec un bit setuid ni des capacités de fichier. | Cas de recette « Préparation des nœuds », « Abandon des privilèges » et « Abandon en échec ». |
| **S7. Privilèges minimaux.** En conteneur : `cap_drop: ALL` puis seulement les capacités de la phase de préparation (section 6.5), `no-new-privileges`, racine en lecture seule, accès aux périphériques limité par `device_cgroup_rules` aux nœuds caractère en lecture. En natif : une unité unique démarrée en root avec `CapabilityBoundingSet` limité au même ensemble, `NoNewPrivileges=yes`, `ProtectSystem=strict`, `DevicePolicy=closed` avec `DeviceAllow=char-aac rm` (création et lecture) (et équivalents par pilote), `SystemCallFilter`. Ni `userns-remap` ni Docker rootless : les capacités y sont sans effet sur les contrôles `capable()` des pilotes. `privileged: true` n’est qu’un repli documenté (section 6.5). | Le fragment Compose et l’unité systemd fournis respectent ces règles. Avec `userns-remap`, l’exporter publie une erreur d’accès explicite (cause `EPERM`). |
| **S8. Commandes rejouables.** Seules des commandes de consultation idempotentes figurent dans la liste blanche, car une commande interrompue par un signal peut être réémise par le noyau. Le transport traite `EINTR` comme un échec de la lecture en cours, sans nouvel essai immédiat. | Revue de chaque entrée de liste blanche. Test sur faux appel système renvoyant `EINTR`. |
| **S9. Exposition HTTP maîtrisée.** Le service ne propose aucune action sur le matériel : seul `/metrics` (et une page d’accueil statique) est routé. TLS et authentification ne sont pas intégrés : l’écoute par défaut est locale (`127.0.0.1`) ; une exposition au-delà de l’hôte passe par un réseau maîtrisé (réseau Docker partagé avec Prometheus, pare-feu) ou un mandataire TLS. Les journaux ne contiennent ni série ni WWN complets. | Test de routage. Revue des journaux de recette. |

Pour `megaraid_sas`, la capacité `CAP_SYS_ADMIN` n’est exigée qu’à l’ouverture du nœud et non à chaque ioctl. Le module devra faire ouvrir ce nœud par l’étape privilégiée de lancement, puis transmettre le descripteur au processus de collecte, qui reste sans `CAP_SYS_ADMIN`. La liste blanche porte sur les DCMD de consultation (par exemple `MR_DCMD_CTRL_GET_INFO`, `MR_DCMD_LD_LIST_QUERY`, `MR_DCMD_PD_LIST_QUERY`). Pour `hpsa` et `smartpqi`, elle porte sur les commandes CISS de consultation.

## 5. Contrat Prometheus

Les noms ci-dessous sont proposés pour la première version. La liste exacte des métriques publiées, de leurs labels et des vocabulaires normalisés communs à tous les pilotes est tenue dans [docs/metrics.md](../docs/metrics.md), qui fait référence ; les règles d'alerte d'exemple sont dans [docs/alerts.md](../docs/alerts.md). Le label `controller` identifie la carte sur toutes ses métriques. Employer `serial:<série>` lorsque le numéro de série matériel du contrôleur est valide et unique sur l’hôte. Conserver les zéros initiaux. Ignorer les valeurs vides et les sentinelles constructeur. Si la série est définitivement indisponible ou dupliquée, employer `pci:<domaine:bus:slot.fonction>` pour chacune des cartes concernées. Résoudre les collisions sur l’inventaire complet, quel que soit l’ordre de découverte. Les identifiants PCI vendeur et périphérique désignent un modèle et ne distinguent pas deux exemplaires. L’adresse PCI complète (BDF) identifie un emplacement et peut changer après une modification de topologie. Conserver `pci_address` et `identity_source` sur `hwraid_controller_info`. Une erreur de lecture temporaire conserve l’identité déjà établie ; à froid, différer la publication des séries métier à identité indéterminée et signaler l’erreur de découverte. Utiliser un ensemble borné d’états normalisés. Les libellés du firmware non reconnus deviennent `unknown` et sont consignés dans les journaux. [Conventions d’écriture d’un exporter](https://prometheus.io/docs/instrumenting/writing_exporters/)

| Famille | Labels | Sens |
|---|---|---|
| `hwraid_discovery_success` | — | 1 si la dernière découverte a abouti. |
| `hwraid_snapshot_ready` | — | 1 quand `/metrics` sert la dernière collecte ; 0, sans valeur matérielle, avant la première, pendant l’arrêt et quand la vue est périmée. |
| `hwraid_controller_info` | `controller`, `identity_source`, `pci_address`, `driver`, `backend`, `model`, `firmware` | Inventaire, valeur 1. |
| `hwraid_controller_backend_supported` | `controller` | 1 si un module sait collecter cette carte. |
| `hwraid_management_node_valid` | `controller` | 0 si le nœud dédié est absent ou ne correspond pas à la carte (erreur de déploiement). |
| `hwraid_collector_supported` | `controller`, `collector` | 0 pour une fonction explicitement non prise en charge par le module. |
| `hwraid_collection_success` | `controller`, `collector` | Résultat de la dernière lecture de la fonction. |
| `hwraid_collection_last_success_timestamp_seconds` | `controller`, `collector` | Date Unix du dernier succès. |
| `hwraid_collection_interval_seconds` | `controller` | Intervalle effectif de collecte. |
| `hwraid_controller_state`, `hwraid_volume_state`, `hwraid_disk_state`, `hwraid_cache_protection_state` | objet + `state` | État observé, valeur 1. |
| `hwraid_volume_info`, `hwraid_volume_member`, `hwraid_disk_info` | objet ; `raid_level` normalisé et `vendor_level` pour les volumes, `member` (disque ou volume) pour les relations | Relations et inventaire, valeur 1. |
| `hwraid_volume_write_cache_policy` | volume ; `policy` normalisé et `vendor_policy` | Réglage configuré du cache d’écriture, valeur 1. |
| `hwraid_volume_capacity_bytes`, `hwraid_volume_write_cache_enabled` (0 ou 1), `hwraid_task_progress_ratio`, `hwraid_cache_protection_present` (`type` normalisé, `vendor_type`), `hwraid_controller_temperature_celsius` | objet | Mesures. |

Extrait synthétique d’un même scrape. Deux cartes possèdent chacune un volume 0. La première reconstruit son volume et porte un ZMM. Dans cet exemple illustratif, le futur module MegaRAID de la seconde carte signale explicitement l’absence du module.

```prometheus
hwraid_snapshot_ready 1
hwraid_discovery_success 1
hwraid_controller_info{controller="serial:EXAMPLE6805A01",identity_source="serial",pci_address="0000:01:00.0",driver="aacraid",backend="aacraid",model="6805",firmware="5.3-0[19204]"} 1
hwraid_controller_info{controller="pci:0000:05:00.0",identity_source="pci",pci_address="0000:05:00.0",driver="megaraid_sas",backend="megaraid",model="example",firmware="example"} 1
hwraid_controller_backend_supported{controller="serial:EXAMPLE6805A01"} 1
hwraid_controller_backend_supported{controller="pci:0000:05:00.0"} 1
hwraid_management_node_valid{controller="serial:EXAMPLE6805A01"} 1
hwraid_collection_success{controller="serial:EXAMPLE6805A01",collector="volumes"} 1
hwraid_volume_info{controller="serial:EXAMPLE6805A01",volume="1d000000",name="Array0",raid_level="concat",vendor_level="RAID_volume",parent=""} 1
hwraid_volume_info{controller="serial:EXAMPLE6805A01",volume="1d000004",name="Array0",raid_level="raid1",vendor_level="1",parent="1d000000"} 1
hwraid_volume_state{controller="serial:EXAMPLE6805A01",volume="1d000004",state="degraded_rebuilding"} 1
hwraid_volume_state{controller="serial:EXAMPLE6805A01",volume="1d000000",state="unknown"} 1
hwraid_volume_state{controller="pci:0000:05:00.0",volume="0",state="optimal"} 1
hwraid_task_progress_ratio{controller="serial:EXAMPLE6805A01",volume="1d000004",task="rebuild"} 0.42
hwraid_volume_member{controller="serial:EXAMPLE6805A01",volume="1d000004",member="d3"} 1
hwraid_volume_member{controller="serial:EXAMPLE6805A01",volume="1d000004",member="d7"} 1
hwraid_disk_state{controller="serial:EXAMPLE6805A01",disk="d7",state="rebuilding"} 1
hwraid_cache_protection_present{controller="serial:EXAMPLE6805A01",type="flash_supercap",vendor_type="zmm"} 1
hwraid_cache_protection_state{controller="serial:EXAMPLE6805A01",state="optimal"} 1
hwraid_cache_protection_present{controller="pci:0000:05:00.0",type="battery",vendor_type="bbu"} 0
hwraid_collection_success{controller="pci:0000:05:00.0",collector="cache_protection"} 1
```

Chaque mesure d’état expose uniquement l’état observé avec la valeur 1. Un nouvel instantané remplace le précédent. Le ratio de progression est compris entre 0 et 1 ; il est omis lorsqu’aucune tâche ne fournit de progression. Pour les tâches sans cible volume, prévoir une famille dédiée à leur type de cible.

| Résultat de la lecture ZMM | Publication attendue pour la carte |
|---|---|
| Module absent, confirmé | `present=0`, collecte réussie, série d’état omise |
| Présent : optimal, en charge ou non prêt (`charging`), en panne | `present=1`, état reconnu, collecte réussie |
| Mot sans le bit d’installation, avec ou sans autres bits | comme l’outil (« ZMM not installed ») : `present=0`, collecte réussie, série d’état omise |
| Lecture en échec | Collecte à 0, aucune valeur d’état ou de présence issue de l’échantillon en échec |
| Fonction explicitement non prise en charge | `hwraid_collector_supported{collector="cache_protection"}=0`, présence et état omis |

Une lecture en échec retire les valeurs métier de la fonction concernée : l’exporter ne publie pas de valeur qu’il n’a pas observée. Les règles d’alerte doivent donc tolérer un trou ponctuel, par exemple un dépassement d’échéance causé par un outil constructeur (section 6.3), et alerter séparément sur la perte de visibilité :

```yaml
# Panne RAID : état publié ; l'alerte déclenchée survit 10 min à un échec
# de lecture ponctuel, sans jamais reprendre une valeur périmée.
- alert: HwraidVolumeDegraded
  expr: hwraid_volume_state{state=~"degraded|failed"} == 1
  for: 5m
  keep_firing_for: 10m
```

```promql
# Perte de visibilité sur les volumes (à combiner avec for: dans la règle)
hwraid_collection_success{collector="volumes"} == 0
```

```promql
# Données périmées malgré des réponses HTTP 200
time() - hwraid_collection_last_success_timestamp_seconds
  > on (job, instance, controller) group_left 3 * hwraid_collection_interval_seconds
```

Le signal Prometheus `up` couvre l’accessibilité HTTP de l’exporter. Une règle d’absence de carte attendue doit s’appuyer sur l’inventaire voulu du site : une carte disparue ne peut plus signaler sa propre erreur.

## 6. Architecture et déploiement

Le service d’inventaire recoupe les fonctions PCI, leurs identifiants constructeur et sous-système, les pilotes liés et les hôtes SCSI visibles dans sysfs. Plusieurs hôtes SCSI peuvent dépendre de la même carte. Dédupliquer les chemins par leur parent matériel et conserver une identité non ambiguë sur l’hôte. Une carte est inventoriée si sa classe PCI est « contrôleur RAID » (`0x0104xx`) ou si un module la revendique : une carte RAID sans module apparaît avec `hwraid_controller_backend_supported 0`, un contrôleur SATA ordinaire n’apparaît pas. La classe PCI RAID reste un indice parmi d’autres : prévoir les interfaces SAS et les contrôleurs configurables en mode HBA. Un HBA identifié reste visible avec les fonctions RAID déclarées inapplicables lorsque son mode est connu. [Interfaces PCI de Linux](https://docs.kernel.org/PCI/sysfs-pci.html) Isoler transport, décodage et exposition HTTP pour pouvoir rejouer une réponse enregistrée sans accès matériel. Le cœur, les décodeurs et les modules de collecte sont écrits en Go et compilés dans un seul exécutable Linux statique.

### 6.1 Relier la carte découverte à son interface de gestion

Parcourir `/sys/bus/pci/devices`, lire les identifiants PCI et résoudre le lien `driver`. Pour chaque entrée de `/sys/class/scsi_host`, remonter le chemin réel jusqu’à la fonction PCI parente. Conserver le domaine dans l’adresse (exemple `0000:01:00.0`). Cette adresse relie les deux inventaires pour la découverte en cours. Le label durable suit la politique de la section 5. Refaire le rattachement après retrait, réapparition ou renumérotation avant de publier de nouvelles observations.

| Pilote | Comment sélectionner la carte pendant la collecte | Origine de l’identité durable |
|---|---|---|
| `aacraid` | Nœud dédié `aac<unique_id>` (section 6.2). Après ouverture, `FSACTL_GET_HBA_INFO` confirme le numéro de carte, le bus, le périphérique et la fonction PCI (sans domaine). | `hostH/serial_number` dans sysfs (section 3.2). Valeur vide possible. |
| `megaraid_sas` | Nœud dédié `megaraid_sas_ioctl`, partagé par toutes les cartes. Le paquet ioctl porte `host_no`, tiré du `hostH` rattaché à la carte PCI. | `serial_no[32]` de `megasas_ctrl_info`. Valider sa valeur pour chaque firmware retenu. [Pilote](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/megaraid/megaraid_sas_base.c) |
| `hpsa` | Nœud dédié vers le périphérique SCSI de type RAID de la carte. `CCISS_GETPCIINFO` confirme domaine, bus, périphérique et fonction. | Repli PCI tant qu’une lecture fiable de la série reste à qualifier. Le `board_id` retourné renseigne le modèle. [Pilote](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/hpsa.c) |
| `smartpqi` | Même principe et même vérification via `CCISS_GETPCIINFO`. | `hostH/serial_number` dans sysfs (attribut d’hôte, série du contrôleur). [Pilote](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/smartpqi/smartpqi_init.c) |

À chaque cycle, le transport ouvre le nœud dédié, compare `st_rdev` obtenu par `fstat` sur le descripteur ouvert à la valeur attendue (`/proc/devices`, `unique_id`, `dev` du périphérique SCSI), effectue ses lectures puis referme le nœud. Cette vérification sur le descripteur, et non sur le chemin, évite qu’un remplacement du nœud entre la vérification et l’ouverture passe inaperçu.

Les droits exigés diffèrent par pilote : `CAP_SYS_RAWIO` pour `aacraid`, `hpsa` et `smartpqi` (commandes de passage), `CAP_SYS_ADMIN` à l’ouverture du nœud `megaraid_sas` (section 4). En cas de rattachement ambigu, garder la fonction PCI visible, consigner les candidats et suspendre les lectures de cette carte. Un nœud absent ou dont le major/minor ne correspond pas à la carte attendue ne peut plus être corrigé après l’abandon des privilèges. Si le nœud était valide au démarrage et ne l’est plus (rechargement de module, carte retirée ou ajoutée), l’exporter journalise la cause et s’arrête avec un code de sortie dédié : le superviseur (Docker `restart: unless-stopped`, systemd `Restart=on-failure`) le relance et la phase de préparation recrée les bons nœuds. Si le nœud est déjà invalide juste après la préparation, l’exporter ne s’arrête pas, pour éviter une boucle de relances : il publie `hwraid_management_node_valid 0`, journalise la cause et ne lit pas la carte.

### 6.2 Nœuds de gestion dédiés à l’exporter

L’exporter dispose de ses propres nœuds de gestion dans `/dev/hwraid_exporter/`. Les autres nœuds de `/dev` appartiennent au système et aux outils constructeurs, qui peuvent les supprimer et les recréer à leur guise (par exemple `/dev/aacN`). L’exporter ne les utilise pas et ne les modifie jamais. Il crée lui-même ses nœuds au démarrage. Le chemin est fixe et non configurable.

Le principe est le même pour tous les pilotes : chaque nœud dédié est un nœud caractère déclaré par le module du pilote (section 6.10), dont le major est lu dans `/proc/devices` sous le nom de pilote déclaré, et dont le nom ne dépend pas de la numérotation `sgN`. La phase de préparation vérifie que le major de chaque nœud correspond à ce nom de pilote, et que le nom respecte une règle générique (minuscules, chiffres, `_`, `.`, `-`, sans barre oblique ni point initial).

| Pilote | Ce que publie le pilote | Nœud dédié | Major / minor |
|---|---|---|---|
| `aacraid` | `register_chrdev(0, "aac")`, aucun nœud créé | `aac<unique_id>` | major `aac` de `/proc/devices`, minor = `unique_id` de l’hôte SCSI |
| `megaraid_sas` | `register_chrdev(0, "megaraid_sas_ioctl")`, aucun nœud créé. Un nœud unique sert toutes les cartes (sélection par `host_no`). | `megaraid_sas_ioctl` | major `megaraid_sas_ioctl` de `/proc/devices`, minor 0 |
| `hpsa`, `smartpqi` | Pas de nœud propre. Les ioctl passent par un périphérique SCSI de la carte. Le contrôleur s’expose lui-même comme périphérique SCSI de type RAID, avec un `/dev/sgN` créé par udev mais dont le numéro varie. Nécessite le module `sg`. | `<pilote>-<domaine>_<bus>_<slot>.<fonction>` | `dev` de `/sys/class/scsi_generic/sgN` du périphérique de type RAID rattaché à la carte |

**Phase de préparation.** Au démarrage, avant de lire toute entrée externe et avant d’ouvrir le port HTTP, l’exporter s’exécute en root avec un jeu réduit de capacités (`CAP_MKNOD`, `CAP_CHOWN`, `CAP_SETUID`, `CAP_SETGID`, `CAP_SETPCAP`, plus celles des pilotes, `CAP_SYS_RAWIO` pour `aacraid`). Il prend un verrou exclusif (`flock`), énumère les cartes prises en charge, calcule l’ensemble des nœuds attendus, puis :

1. crée `/dev/hwraid_exporter/` (propriétaire root, groupe de l’identité d’exécution, mode 0750) ou corrige le propriétaire et le mode d’un répertoire existant. Il échoue si ce chemin est un lien symbolique ou n’est pas un répertoire ;
2. n’accepte que les majors résolus par nom dans `/proc/devices` pour les pilotes connus (`aac`, `megaraid_sas_ioctl`, `sg`) et ne crée que des nœuds caractère, jamais de nœud bloc ;
3. crée chaque nœud manquant en mode 0600, avec pour propriétaire l’identité d’exécution non root (par défaut l’UID 65534), par création sous un nom temporaire puis `rename` atomique ;
4. corrige le propriétaire et le mode d’un nœud existant au bon major/minor, et remplace atomiquement un nœud dont le major/minor a changé ;
5. supprime les nœuds caractère du répertoire dont le nom suit son schéma de nommage et qui ne font plus partie de l’ensemble attendu ;
6. refuse de toucher à toute autre entrée (fichier, lien, répertoire, nœud bloc, nom inconnu) : elle est signalée et le démarrage échoue ;
7. n’agit jamais en dehors de `/dev/hwraid_exporter/`, et journalise chaque création, correction et suppression.

**Abandon des privilèges.** La préparation terminée, l’exporter abandonne ses privilèges de façon irréversible, pour tous ses threads (bibliothèque standard de Go : changements d’identité appliqués à tous les threads depuis Go 1.16, capacités et `prctl` par `syscall.AllThreadsSyscall`) : groupes supplémentaires vidés, passage à l’identité non root, capacités réduites à celles des pilotes présents dans tous les ensembles, `no_new_privs`. Il relit `/proc/self/status` et s’arrête si l’identité ou un ensemble de capacités diffère de l’attendu. Le processus de collecte, qui tourne en permanence et répond sur le réseau, ne possède donc jamais `CAP_MKNOD` ni `CAP_SYS_ADMIN` : avec la première et un répertoire inscriptible, un processus compromis pourrait créer un nœud vers n’importe quel disque de l’hôte et le lire en entier. Après l’abandon, il ne peut ouvrir que les nœuds qui lui appartiennent et ceux lisibles par tous.

Ce modèle tient en un seul processus et une seule déclaration, sans composant sur l’hôte :

- **Conteneur** : les nœuds sont créés dans le `/dev` propre au conteneur, un tmpfs recréé à chaque démarrage du conteneur. Ils ne touchent ni l’hôte ni les nœuds des outils constructeurs. L’ordre de démarrage de l’hôte n’a plus d’importance.
- **Natif** : une unité systemd unique démarre l’exporter en root avec les capacités de préparation (S7). Les nœuds sont créés dans `/dev/hwraid_exporter/` sur l’hôte. Ce répertoire est placé dans `/dev` parce que le noyau refuse d’ouvrir un nœud de périphérique situé sur un système de fichiers monté avec l’option `nodev`, ce qui est le cas de `/run` sur Debian.

Le major dynamique, le `unique_id` attribué au probe et la numérotation `sgN` peuvent changer sans redémarrage de l’hôte. L’exporter le détecte par la vérification de la section 6.1, et la relance par le superviseur suffit à remettre les nœuds en état.

Pour `megaraid_sas`, dont l’ouverture exige `CAP_SYS_ADMIN`, la phase de préparation ouvrira le nœud avant l’abandon des privilèges et conservera le descripteur. Ce point sera qualifié avec ce module.

### 6.3 Cohabitation avec les outils constructeurs

Un outil constructeur (ARCCONF, maxView, son service CIM) peut être installé et actif. L’exporter doit fonctionner de la même façon avec ou sans lui :

- il n’utilise ni ne modifie les nœuds de `/dev` gérés par ces outils ;
- il tolère que ses commandes soient retardées par celles d’un autre processus sur la même carte (sérialisation du pilote, section 3.1) : une échéance dépassée pour cette raison est une collecte en échec ordinaire, sans effet sur les autres cartes ;
- il n’ouvre aucun abonnement aux événements de la carte.

### 6.4 Collecte autonome et publication du snapshot

L’ordonnanceur lance la découverte et la collecte au démarrage, puis aux échéances internes de l’exporter. Il poursuit ses cycles lorsque Prometheus est arrêté. Chaque carte a au plus une lecture de gestion en vol. Une échéance manquée est coalescée pour éviter une file d’attente croissante. Une carte lente ou bloquée doit laisser aux autres la possibilité de progresser.

Le producteur construit des résultats immuables par carte et par fonction. Un éditeur unique assemble une nouvelle vue complète et la publie par échange atomique. Les cartes peuvent avoir des dates d’observation différentes. Les séries devenues invalides sont retirées lors de la publication, notamment celles d’un contrôleur dont la disparition est confirmée par une découverte réussie. Une erreur de découverte est signalée séparément.

Le gestionnaire HTTP charge une seule référence au snapshot publié pour toute la réponse. Son seul travail est de servir cette vue. Tout accès matériel, y compris découverte sysfs, ioctl et rafraîchissement de données expirées, appartient aux tâches de fond. Une requête qui arrive pendant une collecte reçoit immédiatement la dernière vue publiée. Plusieurs scrapes entre deux publications observent les mêmes échantillons.

**Démarrage et fraîcheur.** Publier avant l’ouverture HTTP une vue initiale avec `hwraid_snapshot_ready 0` et sans valeurs matérielles. Le premier résultat de découverte publié porte `hwraid_snapshot_ready 1`, y compris en cas d’échec ; `hwraid_discovery_success` indique le succès ou l’échec. Pour chaque couple contrôleur/fonction, publier la date du dernier succès dès qu’un succès existe. Une lecture en échec publie `hwraid_collection_success 0` et retire les valeurs métier de cette fonction. Dimensionner le seuil de péremption par rapport à l’intervalle effectif de chaque contrôleur et à la durée de collecte tolérée.

**Arrêt.** Un appel en cours ne doit pas être interrompu : le pilote `aacraid` ne rend pas le bloc de commande d’un ioctl interrompu par un signal [public] ; sur la carte de référence, ce bloc est resté perdu jusqu’au redémarrage (docs/aacraid-defaut-pilote.md, qui qualifie aussi le risque propre aux grandes commandes). À l’arrêt, aucun nouveau cycle ne démarre et le cycle en cours n’envoie plus de nouvelle commande, la seule exception étant la séquence qui termine un transfert paginé déjà ouvert (suite définie par phase dans la section 6.11, « Annulation » ; aujourd’hui, parcours nominal seul : premier morceau, puis clôture) ; hors cette séquence, aucune commande n’est envoyée ; les commandes en cours sont attendues sans limite interne, avec un avertissement toutes les 10 s ; pendant cette attente, `/metrics` ne sert plus de valeurs matérielles (`hwraid_snapshot_ready 0`). Le serveur HTTP est fermé ensuite. Le seul délai est celui du superviseur (`TimeoutStopSec`, `stop_grace_period`, 30 s dans les exemples), réglé par l’exploitant : au-delà, il tue le processus : l’ioctl en cours ne rend pas son bloc de commande, qui est resté perdu sur la carte de référence.

### 6.5 Accès Docker sur Debian / OpenMediaVault

Le `/sys` d’un conteneur non privilégié est déjà en lecture seule et montre le sysfs de l’hôte : l’exporter l’utilise par défaut. Une racine sysfs configurable reste prévue pour les tests. Les systèmes de fichiers des volumes RAID ne sont requis par aucune de ces opérations.

Fragment Compose de référence pour la 6805 :

```yaml
services:
  hwraid-exporter:
    image: <registre>/hwraid-exporter:<version>
    read_only: true
    cap_drop: [ALL]
    cap_add: [MKNOD, CHOWN, SETUID, SETGID, SETPCAP, SYS_RAWIO]
    device_cgroup_rules:
      - "c *:* r"
    security_opt:
      - no-new-privileges:true
    # Écoute sur l’interface du conteneur (127.0.0.1 par défaut, injoignable
    # depuis l’extérieur du conteneur). Rattacher le conteneur au réseau de
    # Prometheus, ou publier le port en le limitant par pare-feu.
    command: ["serve", "-listen", "0.0.0.0:9799"]
    restart: unless-stopped
    stop_grace_period: 30s
```

`device_cgroup_rules` autorise l’ouverture en lecture des nœuds caractère que la phase de préparation crée dans le `/dev` du conteneur. Le joker est nécessaire parce que le major de `aac` est dynamique. Il ne donne rien au processus de collecte, qui ne peut plus créer de nœud après l’abandon des privilèges. Pour `hpsa` et `smartpqi`, ajouter `"c 21:* rw"` (major fixe de `sg`, ouverture en lecture-écriture imposée par le noyau, section 4, S5). Un profil seccomp minimal, adapté au binaire, est prévu pour remplacer le profil par défaut de Docker (section 7.7).

**Validation sur l’hôte de référence.** Vérifié : le `/dev` du conteneur n’est pas monté `nodev`. Un nœud créé avec la seule capacité `CAP_MKNOD` vers le major de `aac` s’ouvre avec `device_cgroup_rules: "c *:* r"`, et la même ouverture est refusée (`EPERM`) sans cette règle. Vérifié ensuite avec le prototype, dans ce profil exact : après la préparation, le processus tourne en UID et GID 65534 sur tous les champs, sans groupe supplémentaire, avec `CAP_SYS_RAWIO` seule dans les ensembles effectif, permis et limite, des ensembles hérité et ambiant vides et `no_new_privs` actif. Il ouvre son nœud dédié et obtient une réponse de `FSACTL_GET_HBA_INFO` cohérente avec l’adresse PCI et le `unique_id` de la carte.

**Repli si ces points échouent : `privileged: true`.** Le conteneur reçoit alors toutes les capacités, voit tous les périphériques de l’hôte et `/sys` en écriture, sans seccomp ni AppArmor. L’abandon des privilèges en limite une partie des effets : en UID 65534, le processus de collecte ne peut ni écrire dans sysfs (dont `reset_host`) ni ouvrir les disques de l’hôte, qui appartiennent à root. Ce repli reste moins sûr et doit être signalé dans la documentation de déploiement de l’hôte concerné.

### 6.6 Livraison et versionnement

Pour chaque version et architecture prise en charge, produire une seule fois l’exécutable, publier le binaire téléchargeable avec sa somme SHA-256 et copier ces mêmes octets dans l’image correspondante. Première architecture de recette : Linux amd64. Sur Debian/OMV, le lancement direct doit fonctionner en l’absence de Docker. Version, options de démarrage, découverte et contrat Prometheus sont communs aux deux distributions. Sont livrés avec chaque version : le fragment Compose, une unité systemd unique pour le mode natif conforme à la section 4 (S7) et le profil seccomp. Aucun composant n’est à installer sur l’hôte en mode conteneur.

### 6.7 Étude des paramètres de configuration

Objectif de la première version : démarrer avec la découverte automatique de toutes les cartes visibles. Les réglages ci-dessous sont des candidats à évaluer. Les valeurs opérationnelles seront choisies après mesure sur le matériel de référence.

| Sujet | Proposition de départ | Question à trancher |
|---|---|---|
| Écoute HTTP | Adresse et port configurables, chemin standard `/metrics`. En natif, écoute par défaut sur `127.0.0.1`. En conteneur, adresse publiée uniquement sur le réseau du serveur Prometheus. | Documenter l’exposition derrière un proxy. |
| Accès aux périphériques | sysfs sous `/sys` par défaut, racine configurable pour les tests. Nœuds de gestion : `/dev/hwraid_exporter`, chemin fixe (dans le `/dev` du conteneur ou sur l’hôte en natif). Identité d’exécution après abandon des privilèges : UID/GID 65534 par défaut, configurable. | Valeurs définitives après essai sur OMV. |
| Périmètre de collecte | Inventorier toutes les cartes. Étudier l’exclusion explicite d’une carte ou d’une fonction coûteuse. | Comment distinguer dans les métriques une exclusion volontaire, une fonction indisponible et une erreur de lecture ? |
| Cadence de collecte | Intervalle global par défaut et surcharge facultative par contrôleur. | Quelle valeur globale retenir après mesure sur matériel ? |
| Délais et parallélisme | Mesurer la durée des lectures par module et borner la concurrence matérielle. | Quel budget par lecture, en tenant compte des commandes concurrentes d’un outil constructeur ? Le délai HTTP concerne uniquement le service de l’instantané. |
| Découverte et fraîcheur | Mesurer le délai de détection d’une carte ajoutée ou retirée, et exposer la date de dernière observation réussie. | Quelle cadence de redécouverte et quel seuil d’alerte de péremption retenir ? |
| Diagnostic | Affichage de version (`hwraid-exporter version`, `hwraid_exporter_build_info`) ; niveau de journalisation et mode `--dry-run` prévus (section 7.7). | Quels détails suffisent pour diagnostiquer une carte illisible en préservant les identifiants des disques ? |

Privilégier des arguments CLI utilisables directement ou via `command` dans Compose. Évaluer un fichier de configuration uniquement si des règles par contrôleur rendent la CLI peu lisible. Si plusieurs sources sont retenues, documenter leur priorité, les unités, les valeurs par défaut et le traitement des options invalides. La fréquence de scrape et les seuils d’alerte se règlent côté Prometheus. Le choix des périphériques exposés au conteneur relève du déploiement.

**Livrable de l’étude.** Un tableau des options retenues avec leur justification, deux exemples équivalents (binaire et Compose), et des essais couvrant démarrage par défaut, option invalide, carte inaccessible et collecte lente. Comparer les métriques métier sur le même hôte dans les deux modes.

### 6.8 Intervalle global et exceptions par contrôleur

**État : seul l'intervalle global est implémenté** (`-interval`, 1 minute par défaut, publié par `hwraid_collection_interval_seconds`). Les exceptions par contrôleur décrites ci-dessous sont prévues, non implémentées (section 7.7).

Le binaire doit démarrer avec un intervalle global par défaut. Chaque carte découverte en hérite. Une règle associée à la valeur exacte du label stable `controller` remplace cet intervalle pour cette seule carte. La même règle s’applique après redémarrage. Une règle utilisant une adresse PCI suit cet emplacement en cas de remplacement de carte.

Exemple illustratif de configuration. La syntaxe finale et les valeurs par défaut restent à valider :

```yaml
collection:
  interval: 60s
  controllers:
    "serial:EXAMPLE6805A01":
      interval: 5m
```

L’ordonnanceur applique l’intervalle effectif aux cycles de lecture matérielle de chaque carte. Une erreur, un scrape ou une redécouverte de la même carte ne doivent pas lancer un cycle anticipé. Une échéance dépassée pendant un cycle est ignorée, sans rattrapage en rafale. Refuser les durées nulles ou négatives et les règles en double. Une règle visant une carte absente reste disponible pour sa prochaine détection. Le coût d’un cycle et le délai maximal acceptable de détection d’une panne guideront le choix des intervalles.

### 6.9 Construction de l’image Docker

Le binaire est construit une seule fois par la CI (`CGO_ENABLED=0`, `-trimpath`, image Go épinglée par version et digest), puis copié tel quel dans l’image. L’image finale part de `scratch` et ne contient que ce binaire. Les pilotes et le noyau sont ceux de l’hôte.

```dockerfile
FROM scratch
ARG TARGETARCH
COPY dist/hwraid-exporter-linux-${TARGETARCH} /hwraid-exporter
ENTRYPOINT ["/hwraid-exporter"]
```

La CI vérifie que la somme SHA-256 de `/hwraid-exporter` dans l’image est égale à celle du binaire publié.

### 6.10 Contrat d’un module de collecte

Le registre sélectionne un seul module propriétaire de la collecte pour chaque contrôleur, selon le pilote, les identifiants PCI et, si nécessaire, une identification en lecture seule. Une correspondance ambiguë est publiée comme telle. Le cœur assure découverte, planification, identité des objets et exposition HTTP. Le module assure le transport de lecture (dans les limites de la section 4), le décodage et la traduction des états constructeur. Les identifiants locaux de disque et de volume sont toujours rattachés au contrôleur.

```text
Match(carte) → le module prend-il la carte en charge ?
Nodes(carte) → nœuds nécessaires : nom, pilote dont lire le major, minor
Capabilities() → capacités à conserver après l’abandon des privilèges
Functions(carte) → fonctions prises en charge, inconnues ou inapplicables
Collect(carte, nœuds préparés, échéance) → instantané et résultat par fonction
```

`Match`, `Nodes` et `Capabilities` sont des fonctions pures : un module **déclare** ce dont il a besoin et ne crée jamais lui-même de nœud. Le cœur regroupe les hôtes SCSI par carte, associe chaque carte à son module, puis réalise en une seule passe privilégiée la création des nœuds déclarés par tous les modules, avec les contrôles de la section 6.2. Il abandonne ensuite ses privilèges en conservant l’union des capacités déclarées par les modules qui ont au moins une carte. Les ouvertures qui exigent un privilège absent après l’abandon (cas de `megaraid_sas`) sont elles aussi déclarées et réalisées par le cœur pendant la préparation. `Collect` s’exécute après l’abandon. Une carte revendiquée par plusieurs modules, ou deux cartes déclarant le même nom de nœud, sont signalées en erreur et n’obtiennent aucun nœud.

Les familles communes de métriques utilisent le préfixe `hwraid_`. La protection du cache couvre ZMM, batterie ou autre module identifié. Exposer son type dans l’inventaire et publier l’état normalisé uniquement si la correspondance est établie. Une carte sans module de collecte donne `hwraid_controller_backend_supported=0` avec son identité. Une carte prise en charge mais illisible conserve `backend_supported=1` et signale une collecte en échec. Les fonctions encore inconnues restent indéterminées. Utiliser un module de simulation pour tester cette distinction avant la disponibilité d’un deuxième matériel.

Les lectures directes de SMART par disque, les fonctions d’administration RAID et l’embarquement d’un outil constructeur sont hors périmètre.

### 6.11 Généralisation des échanges `aacraid`

**État : conception, non implémentée.** Aujourd’hui, l’exporter reproduit les requêtes de l’outil témoin telles qu’il les envoie à la carte de référence : les valeurs qui dépendent de la carte y sont figées (taille de la liste des conteneurs, taille des blocs de commande). Cette section les fait calculer, comme l’outil, à partir des capacités annoncées par la carte. Sur le modèle qualifié et les réponses nominales, les données décodées, les niveaux RAID, les états et les métriques ne changent pas. Sont ajoutés : la validation des nouvelles phases de transfert, le filtre de la 708, le décodage de la batterie classique et la valeur d’état `maintenance`.

#### Règle de conduite

**La carte de référence est maîtrisée ; ailleurs, l’exporter suit l’outil.** Dans les décisions de lecture des volumes et des disques examinées (versions 2.05 et 3.07), l’outil ne fait dépendre ses requêtes ni du modèle, ni des identifiants PCI, ni de la version de firmware : il les construit à partir des capacités de la carte (réponses 703 et 706, `MaxContainers`) et de ses réponses (section 7 de `docs/aacraid-interface.md`). L’exporter fait de même, avec trois règles :

1. **Jamais plus que l’outil.** Dans une situation donnée, l’exporter n’envoie aucune commande ni transition que l’outil n’enverrait pas, sauf les écarts du tableau ci-dessous.
2. **Moins, quand une réponse est rejetée.** Un contrôle de l’exporter (statut, écho, identifiant du transfert, numéro de morceau, tailles, bornes, prérequis) peut arrêter la suite : la lecture est en échec, et les lectures qui en dépendent ne sont pas envoyées au cycle courant. Sur un échec d'échange d'un morceau, aucune commande de ce transfert ne suit, pas même une clôture, comme l'outil (voir « Abandon sans clôture ») ; sur un écart que seul l'exporter contrôle dans un morceau ou les données d'un parcours `0xc9` déjà ouvert et validé, seule la clôture suit, que l'outil aurait atteinte (une ouverture refusée n'est suivie d'aucune commande, un parcours `0xc8` interrompu n'a pas de clôture). Quand la fin du transfert n'est pas établie (ouverture refusée alors qu'un identifiant de transfert a été reçu, ou dont l'en-tête est refusé ; parcours `0xc8` interrompu, dernier morceau refusé compris ; clôture sans `0xc6` ni `0xc5` du bon transfert ; commande refusée avant son envoi dans un transfert ouvert), plus aucune commande ne part vers la carte pendant le cycle (2026-11-27 ; `docs/aacraid-interface.md`, section 4).
3. **Données plus strictes.** Les données ne sont utilisées que selon « Données retenues » : un code d’ouverture autre que `0xda` rejette les données ; il abrège la suite de commandes en une clôture sans morceau (ligne 6, moins de commandes que l’outil).

Les mesures qui ne concernent pas la carte (nœuds dédiés, abandon des privilèges, sérialisation, une lecture en vol par carte) valent pour toutes les cartes.

| Écart | Effet sur les envois | Portée | Statut | Raison |
|---|---|---|---|---|
| E1. Arrêt anticipé | clôture dès que les données suffisent, avant le dernier morceau | couple validé seulement | validé sur 5.3-0[19204] | moins de commandes par lecture, donc moins de temps de carte occupée (les commandes de tous les outils passent une par une) ; accepté par ce firmware |
| E2. Relecture après une clôture `0xc5` | une nouvelle ouverture, au plus une fois par lecture, que l’outil ne fait pas | couple validé seulement | comportement actuel sur la carte de référence | seule exception à « jamais plus que l’outil » : `0xc5` arrive après un échange complet (ioctl réussis, morceaux conformes), observée deux fois sur environ 1 400 transferts pendant que maxView interrogeait la carte ; sans relecture, la fonction serait en échec pour tout le cycle ; aucun risque connu pour le pilote, à la différence d’une relance après un ioctl en échec |
| E3. Borne de taille | total supérieur à 4 100 octets hors code `0xe2` : abandon (aucune commande) au lieu de la lecture des morceaux | toutes les cartes | comportement actuel ; une clôture immédiate ne remplacera l’abandon que lorsque sa forme sera établie (requête de clôture construite sur un total hors borne : aucune preuve) | les morceaux envoyés restent dans les tailles observées ; aucune forme établie pour clore un tel transfert |
| E4. Annulation (échéance du cycle, arrêt du service) | suite définie par phase (tableau des événements) | toutes les cartes | l’outil n’a pas d’échéance | l’arrêt du service ne doit ni interrompre un ioctl (bloc de commande perdu, défaut du pilote) ni laisser un transfert ouvert sans nécessité |

Aucune option ne permet d’appliquer E1 ou E2 à une autre carte : pour en bénéficier, on qualifie le couple modèle–firmware (capture, comparaison à l’outil, ajout à la liste).

**Non-régression.** Sur le modèle qualifié et sur les réponses nominales capturées, les capacités redonnent exactement les requêtes actuelles, qu’un test compare (séquence et octets) au transport actuel, avec un seul ajout attendu : `CT_GET_CONTAINER_COUNT`, publique (pilote Linux), que l’outil témoin envoie à la carte de référence (captures) ; elle relève de S4 sans dérogation et sa réponse est gardée comme celle de la 706. La séquence de référence est celle du transport actuel, enregistrée par un test sur faux pilote avant toute modification (sur deux cycles, avec et sans réponses gardées) ; `CT_GET_CONTAINER_COUNT` y est insérée avant la 229, les deux requêtes de la protection du cache y sont déplacées après la 803 de chaque cycle, la 704 est insérée après `GET_HBA_INFO` à chaque cycle et, au premier, la 701 puis la 703 avant la 706. Les nouvelles branches sont testées séparément.

#### Lectures et conditions d’envoi

Verrou commun : **aucune commande firmware** n’est envoyée si le modèle n’est pas qualifié et que la carte n’est pas désignée (section 6.12) ; seul `GET_HBA_INFO`, traité par le pilote, est lu. Un test sur faux pilote le vérifie sur une carte qui annonce toutes les lectures.

**Ouverture de la carte (implémenté), comme l’outil (versions 2.05 et 3.07) :** 704 à chaque cycle (carte en pause : rien d’autre) ; 701 (réponse refusée ou build inférieur à 2120 : rien d’autre ; acceptée, gardée comme la 703) ; 703 ; 706 seulement si la 703 annonce le bit 16 de `SupportedOptions` (même condition dans l’énumération et l’ouverture de l’outil, et dans le pilote Linux). Un échec de l’une de ces quatre commandes arrête le cycle de la carte ; l’outil abandonne aussi l’ouverture quand l’ioctl de l’une d’elles échoue (son traitement d’un rejet par le firmware, ioctl réussi, reste à établir). La 703 et la 706 de l’exporter reprennent la forme que l’outil envoie à chaque exécution lors de l’énumération des cartes (`XferState` = 1, en service depuis la v0.1.0) ; à l’ouverture, il les envoie aussi avec `XferState` = `0x100001`, comme la 704 et la 701 : les deux formes sont des requêtes de l’outil. **Reste documenté (décision du 2026-11-26) :** après la 706, l’ouverture de l’outil envoie d’autres commandes (dont 905, 707 et la lecture des informations du bus), dont les données ne servent pas à l’exporter, et peut encore échouer ; l’exporter ne les envoie pas et lit ensuite la protection du cache et le reste là où l’outil aurait pu abandonner (point 20 de la section 7.7). Les réponses 701, 703 et 706 sont gardées tant que l’identité de la carte ne change pas, alors que l’outil les relit à chaque consultation : elles dépendent du firmware et du matériel, et une mise à jour du firmware (identité changée) ou une réinitialisation de la carte (nœud changé) les fait relire, ce qu’un test vérifie. Formes : `docs/aacraid-interface.md`, section 5. Au-delà, chaque commande suit la condition de l’outil (versions 2.05 et 3.07) ci-dessous, complétée par les prérequis propres à l’exporter (plus bas) ; l’échec d’une lecture interdit au cycle courant celles qui en dépendent.

Ordre observé de l’outil, en résumé (avec des répétitions) : 703, 706 et 906 à l’énumération, puis 704, 701, 703 et 706 à chaque ouverture de la carte, 803, `CT_GET_CONTAINER_COUNT`, 708, 247 puis les 248, de nouveau `CT_GET_CONTAINER_COUNT`, 229 puis les 234 puis les 249, l’alarme sonore (190), enfin la protection du cache. L’exporter suit cet ordre (implémenté le 2026-11-26, audit T5 à T7 ; alarme le 2026-11-27) : 803, comptage, 708 quand elle doit être lue, 247 puis les 248 dans l’ordre de la liste rendue, 229, les 234, les 249, puis l’alarme, la protection du cache et les tâches ; il ne fait qu’une passe (l’outil en fait deux). L’ordre compte : l’outil abandonne l’ouverture de la carte sur un échec d’échange (ioctl, statut ou écho) pendant ces lectures, et sur un comptage en échec ou hors bornes ; l’exporter s’arrête alors au même point. Après un refus (code, ou contrôle de l’exporter), il poursuit la boucle comme l’outil (248, 234 et 249 suivantes ; 249 des conteneurs retenus même après un refus de la 247), pour qu’un échec d’échange sur un objet suivant l’arrête au même point. **Restes documentés**, où l’exporter ne peut pas savoir que l’outil aurait abandonné, faute d’envoyer la commande qui aurait échoué : la seconde liste des conteneurs (229 de paramètre 1), la 247 de comptage et la seconde passe de `buildChildren`, la suite de l’ouverture (point 20 de la section 7.7 ; sorties listées dans la matrice des preuves), le rafraîchissement de la 706 et de la 905 dans les informations générales (son échec empêche la protection du cache chez l’outil), le comptage en blocs de 2 048 octets (sous le bit 15, l’exporter lit encore la protection du cache ; sous le mode particulier, rien : à harmoniser si une carte qui les annonce apparaît), `0xa0000` ; deux conditions internes de l’outil sans équivalent observable (limite `ctx+0xaac` avant le comptage, gestionnaire de clignotement avant la 708) ; l’ordre des 249 quand un enfant précède sa racine dans la liste (l’outil lit les racines d’abord ; jamais observé) ; une 103 pour un conteneur que l’outil n’affiche pas (probable, à confirmer sur carte émulée avec une tâche active).

| Lecture | Condition d’envoi | Forme |
|---|---|---|
| Vérification du nœud (`GET_HBA_INFO`) | aucune | actuelle, avant toute commande firmware |
| 706 | nœud vérifié | actuelle (lue ou réutilisée) |
| 703 `RequestAdapterInfo` (publique) | 706 valide | actuelle |
| `CT_GET_CONTAINER_COUNT` (publique, envoyée aussi par le pilote Linux au chargement) | toute carte lue, à l’ouverture après la 803 (sauf bit 28 de `SupportedOptions2` ou blocs de 2 048 octets) ; décodé si `MaxContainers` est de 1 à 1023, carte ouverte si de 1 à 256 | `VM_ContainerConfig`, sous-commande 4, avec les variantes des formes 2 et 3 ; mot 2 de la réponse : `MaxContainers` |
| 229, 234, 249 (volumes et membres) | 706 avec `Version` > 1 et `SupportedOptions2` bit `0x08`, `FeatureBits` bit 9 (`0x200`) et `MaxContainers` ≥ 1 | voir « Formes » |
| 247, 248 (disques) | 706 avec `Version` > 1 et `SupportedOptions2` bit `0x08`, sans condition supplémentaire ; lue avant les volumes, comme l’outil ; un refus de la chaîne des volumes ne l’interrompt pas, et inversement ; un échec d’échange arrête la carte (voir « Dépendances des résultats ») (implémenté ; succès et échecs attribués par collecteur) | voir « Formes » |
| 803 (température) | `SupportedOptions` (703) bit `AAC_SUPPORTED_HEAT_SENSOR` | actuelle (bloc de 512 octets quelle que soit la carte) |
| `CT_ALARM_GET_STATE` (alarme sonore, 190) | `SupportedOptions` (703) bit 11 (`AAC_OPT_ALARM`, `0x800`) ; à chaque cycle, après les listes des membres et juste avant la protection du cache (place du second envoi de l’outil, `refreshAlarmState`) ; ni sous le bit 28 de `SupportedOptions2`, ni avec les blocs de 2 048 octets (l’outil l’enverrait dans cette forme, non implémentée) | `VM_ContainerConfig`, sous-commande 190, mots `2, 190, 1, 0, 0, 0xffffffff, 0, 8` (forme 1), mot 7 `| 0x80000000` sous le bit 13 (forme 2) ; réponse : statut, écho 190 et mot 2 = 1 exigés comme par l’outil, `Size` 496 en plus ; état : bit `0x02` du premier octet de `_ALARM_INFO` (offset `0x20` des données) |
| 708 (carte des connecteurs) | `FeatureBits` (706) bit 8 (`0x100`), lu seulement si `Version` > 1 (règle commune à tout usage de `FeatureBits`) | actuelle (bloc de 2 048 octets quelle que soit la carte) ; filtre de l’outil appliqué : réponse retenue si mot 0 = 1 et nombres d’entrées et de connecteurs non nuls ; envoyée avant la 247 quand elle doit être lue, et jamais une cause d’arrêt de la carte (l’outil l’envoie sans exception et poursuit) ; le décodeur ne publie des emplacements que pour deux connecteurs : sur une carte qui en annonce un autre nombre, la lecture est en échec (emplacement des disques non publié) jusqu’à qualification de ce format |
| Protection du cache (`0x30000`, puis `0x700000`) | `0x700000` si la réponse `0x30000` porte le bit `0x2000` (module à mémoire flash) ; sans ce bit, batterie décodée depuis la seule réponse `0x30000` (voir ci-dessous) | actuelle (bloc de 512 octets quelle que soit la carte) |

Sans `FeatureBits` bit 9, l’outil ne lit ni la liste des conteneurs, ni leurs informations, ni leurs membres, mais lit la liste et les informations des disques : les collecteurs des volumes sont non pris en charge, ceux des disques restent lus.

**Prérequis propres à l’exporter**, en plus des conditions de l’outil (règle 2 : moins de commandes, jamais d’autres) :
- 703 en échec : ni `CT_GET_CONTAINER_COUNT` ni lecture paginée, le format des blocs n’étant pas déterminé (implémenté ; avant, les lectures paginées partaient malgré l’échec de la 703) ;
- `CT_GET_CONTAINER_COUNT` : envoyée à toute carte lue, après la 803, comme à l’ouverture de l’outil (sans condition de bit 9 ni de lectures ARCIO) ; en échec, ou `MaxContainers` hors de 1 à 256 (l’outil compare `MaxContainers` − 1 à `0xff` et n’ouvre pas la carte au-delà), la carte s’arrête pour le cycle ; gardée comme la 706. Sous le bit 28 de `SupportedOptions2` (l’outil compare un autre champ, selon un réglage non établi), ni comptage ni lecture paginée ni protection du cache ; avec les blocs de 2 048 octets (forme 3, non implémentée), ni comptage ni lecture paginée (reste documenté : l’outil enverrait le comptage dans cette forme) ;
- 708 : envoyée avant la 247, comme l’outil, quand aucune n’est gardée pour cette identité ; gardée tant que l’ensemble des disques ne change pas ; s’il change, elle n’est pas relue après les 248 : l’emplacement n’est pas publié à ce cycle et elle est relue au début du suivant ; sans lectures ARCIO, non envoyée ; les conditions supplémentaires de l’outil (gestionnaire de clignotement) n’ont pas d’équivalent ;
- alarme sonore (190) : l’outil l’envoie deux fois par consultation, à l’ouverture (échec : carte abandonnée) et dans le rafraîchissement qui suit la configuration (échec : « Not Available », poursuite) ; l’exporter n’envoie que le second, à sa place, et sur tout échec (ioctl, statut, écho, mot 2 différent de 1, ou réponse refusée par l’exporter : commande, `Size`) publie `alarm` en échec et poursuit, comme l’outil à cet endroit (implémenté le 2026-11-27). L’envoi d’ouverture n’est pas fait : son échec, qui ferait abandonner la carte à l’outil, rejoint le reste documenté du point 20 de la section 7.7. Lue à chaque cycle, jamais gardée : `SETALARM` de l’outil la change sans réinitialiser la carte ;
- protection du cache : envoyée à toute carte lue, après une 703 valide, après la tentative de 803 quand elle est annoncée, même si cette tentative échoue, et après le comptage et les lectures de configuration quand ils ont lieu (implémenté). Point d’analyse 3, établi pour la consultation : l’outil l’envoie à toute carte ouverte ; il quitte avant si la carte n’est pas ouverte ou si la recherche de son contexte échoue (contexte non résolu, mode d’ouverture hors consultation, entrées-sorties suspendues, carte introuvable) ; l’échec de ses lectures d’informations générales est sans effet. Les refus d’ouverture de l’outil (704, 701) sont reproduits (ci-dessus) ; sans le bit `0x2000`, l’outil envoie en plus `0xa0000` si le bit `0x8` est présent, l’exporter ne l’envoie pas (règle 2) ; après un échec d’envoi, l’outil renvoie une fois la même requête, l’exporter ne la renvoie pas.

**Dépendances des résultats.** Un **échec d’échange** (ioctl en échec, statut non nul ou écho incorrect, sur une ouverture, un morceau ou une clôture) pendant les lectures de configuration, comme un comptage en échec ou hors bornes ou une fin de transfert non établie, arrête la carte pour le cycle : aucune commande ensuite (ni alarme, ni protection du cache, ni tâches), les collecteurs déjà conclus gardent leur résultat, les autres sont en échec. Une 708 en échec, même d’échange, n’arrête pas la carte (l’outil l’envoie sans exception) : seul l’emplacement est en échec. Un **refus** (code d’ouverture, ou contrôle propre à l’exporter, transfert clos) n’arrête que ce qui en dépend ; les boucles se poursuivent comme chez l’outil :

| Refus | Lectures non envoyées au cycle | Collecteurs en échec | Collecteurs maintenus |
|---|---|---|---|
| 247 | les 248 | inventaire, emplacement ; volumes et membres (les états utilisent la présence des disques) | 229, les 234 et les 249 des conteneurs retenus sont lues (comme l’outil) |
| une 248 | aucune (les 248 suivantes partent, comme chez l’outil) | inventaire, emplacement | chaîne des volumes ; états calculés avec les 248 lues comme preuves de panne |
| 229 | les 234 et les 249 | volumes, membres | chaîne des disques, déjà lue |
| une 234 | la 249 de ce conteneur (les 234 suivantes et les autres 249 partent) | volumes, membres | chaîne des disques |
| une 249 | aucune (les 249 suivantes partent) | volumes, membres | chaîne des disques |

La publication des états des disques sans données de volumes reste à spécifier (liste ci-dessous).

#### Formes qui dépendent de la carte

[interopérabilité] Établies par l’analyse de l’outil et confirmées en observant son comportement (versions 2.05 et 3.07, mêmes requêtes dans les deux versions) :

1. **Liste des conteneurs (229) : 8 + 4 × `MaxContainers` octets.** Sur la carte de référence, `MaxContainers` = 256, d’où les 1 032 octets actuels. L’exporter vérifie statut, écho et taille de la réponse, borne `MaxContainers` à 1..1023 avant le calcul (4 100 octets au plus), garde la réponse comme celle de la 706 (rattachée à l’identité de la carte) et refuse hors bornes (lecture en échec ; l’outil n’émet aucune liste pour 0 : écart prudent). Le nombre d’entrées reçues reste inférieur ou égal à `MaxContainers`.
2. **`FeatureBits` (706) bit 13 (`0x2000`)** (implémenté) : bit de poids fort (`0x80000000`) du mot 7 (taille demandée) de la requête d’ouverture, de celle de `CT_GET_CONTAINER_COUNT` et de celle de `CT_ALARM_GET_STATE` (190 : analyse statique seule, l’outil appliquant le bit dans sa routine commune d’envoi ; non confirmé sur carte émulée) ; sur le parcours `0xc9`, les requêtes de morceau et de clôture reprennent le total de la réponse, sans ce bit (le parcours `0xc8` suit la forme 4). Sens non établi.
3. **`SupportedOptions` (703) bit 15 (`0x8000`) : blocs de 2 048 octets pour les transferts paginés** (non implémenté) : `FSACTL_SEND_LARGE_FIB`, `Size` = 2 016, `SenderSize` = 2 048, pas de 1 984 octets (au lieu de `FSACTL_SENDFIB`, 480, 512 et 432). La règle vaut pour toutes les requêtes `VM_ContainerConfig` : ouvertures, morceaux, clôtures et `CT_GET_CONTAINER_COUNT` (observé), donc aussi `CT_ALARM_GET_STATE` (190, que l’exporter n’envoie pas sous ce bit) ; les autres commandes (703, 706, 708, 803, protection du cache) gardent leur forme actuelle. Confirmation limitée à l’ioctl et aux tailles d’en-tête ; transfert complet et pas des morceaux établis par l’analyse seule.
4. **Ouverture portant déjà des données (marque `0xc8`)** (implémenté ; mot 6 nul, `SenderSize` d’au plus 64 ou pas supérieur à 432 octets refusé) : l’outil copie les données reçues avec l’ouverture (total au mot 3, pas égal au minimum du mot 6 et de `SenderSize` − 64, et à 1 si le mot 6 est nul), demande les morceaux suivants à partir du numéro 1 avec la taille demandée `| 0x80000000` au mot 7, et ne clôture pas.

#### Déroulement d’un transfert

Après une réponse d’ouverture au statut nul et à l’écho correct. Les lignes sont évaluées dans l’ordre ; la première qui s’applique décide. « Comme l’outil » signifie la même suite de commandes. Toute branche sans données retenues met la lecture en échec.

| # | Réponse d’ouverture | Outil | Exporter, couple validé | Exporter, autre couple | Preuve (S4) |
|---|---|---|---|---|---|
| 1 | identifiant `0xffffffff`, ou marque autre que `0xc8` et `0xc9` (dont `0xc6`) | aucune commande | comme l’outil | comme l’outil | analyse |
| 2 | `0xc8` ou `0xc9`, code `0xe2` au mot 4, quel que soit le total (exporter : total d’au plus 4 100 octets, mot 6 nul, mot 7 égal au total, sinon aucune commande) | clôture immédiate | comme l’outil ; aucune donnée | comme l’outil ; aucune donnée | (b) après `0xc9` (229) et après `0xc8` (247), requête de clôture, réponse non observée |
| 3 | total supérieur à 4 100 octets (E3) | lecture des morceaux | abandon | abandon | sans objet (aucune commande) |
| 4 | `0xc9`, total nul | clôture sans morceau | comme l’outil ; aucune donnée | comme l’outil ; aucune donnée | (b) 229, requête de clôture, réponse non observée |
| 5 | `0xc9`, code `0xda` (nominal) | tous les morceaux, puis clôture | morceaux nécessaires, puis clôture (E1) | tous les morceaux, puis clôture | (a) capture |
| 6 | `0xc9`, autre code | tous les morceaux, puis clôture ; code jugé ensuite par chaque lecture | clôture sans morceau (forme des lignes 2 et 4) ; aucune donnée | idem | (b) forme de clôture des lignes 2 et 4 ; décision du 2026-11-26 : moins de commandes que l’outil, aucun transfert laissé ouvert |
| 7 | `0xc8`, code `0xda`, total de 1 à 4 100 octets | forme 4, sans clôture | comme l’outil, tous les morceaux ; pas nul ou supérieur à 432 octets : aucune commande | idem | (b) morceaux 1 à 9, 247, pas de 432 octets ; taille demandée au mot 7 : analyse statique |

Aujourd’hui, les lignes 1, 2, 4, 5, 6 et 7 sont implémentées (ligne 1 : identifiant `0xffffffff` compris, refusé aussi par la liste blanche dans une requête de morceau ou de clôture ; lignes 2, 4 et 6 : clôture dont les mots 2 à 7 reprennent la réponse d’ouverture, admise par la liste blanche seulement au morceau 0, taille égale au total, total d’au plus 4 100 octets, jamais pour une ouverture `0xc9` / `0xda` à total non nul) ; ligne 7 : pas du mot 6 de 1 à 432 octets (sinon aucune commande), chaque réponse de morceau portant marque `0xc8`, identifiant, numéro, total et `0xda` ; dans le cas de la ligne 3, et pour une ouverture du parcours `0xc9` dont la taille (mot 7) diffère du total, l’exporter n’envoie aucune commande (lecture en échec ; point 19 de la section 7.7). La cible est le tableau ci-dessus.

Événements en cours de transfert :

| Événement | Outil | Exporter, couple validé | Exporter, autre couple |
|---|---|---|---|
| Statut non nul ou mauvais écho sur un morceau (parcours `0xc9`) | abandon sans clôture | abandon sans clôture | abandon sans clôture |
| Retour d’ioctl en échec, `EINTR` compris, sur un morceau | exception, sans clôture (2.05 0x8ddead, 3.07 0x8b0293) | abandon sans clôture, arrêt de la carte pour le cycle | idem |
| Écart que l’outil ne contrôle pas (identifiant, numéro de morceau, marque, code, mot 7, taille de réponse) ou données rejetées | non vu : morceaux suivants, puis clôture (2.05 0x8ddecb..0x8ddef7, 3.07 0x8b02b5..0x8b02dc) | clôture après le morceau reçu, valeurs de l’ouverture validée, sans les morceaux suivants ; lecture en échec | idem |
| Réponse de clôture | statut, écho et marque `0xc6` vérifiés, pas l’identifiant ; autre marque rendue à l’appelant | `0xc6` du bon transfert : transfert clos ; `0xc5` du bon transfert : lecture rejetée ; statut non nul ou mauvais écho : échec d’échange (prioritaire) ; toute autre réponse (autre marque, autre identifiant, en-tête refusé) : fin non établie ; dans les deux cas, arrêt de la carte pour le cycle | idem |
| Clôture `0xc5` | lecture rejetée, non refaite | relecture (E2) seulement sur la ligne 5, après des morceaux conformes, si le contexte admet encore une commande ; après une clôture immédiate ou une annulation : lecture en échec, sans relecture | lecture en échec, sans relecture |

Annulation (E4), selon la dernière phase envoyée ; après une annulation, aucune nouvelle ouverture ni relecture n’est envoyée et les données du transfert sont écartées :

| Phase | Couple validé | Autre couple |
|---|---|---|
| ouverture non encore envoyée | aucune commande | aucune commande |
| ligne 5, aucun morceau envoyé | morceau 0, puis clôture | tous les morceaux, puis clôture |
| ligne 5, morceau n reçu | clôture | morceaux restants, puis clôture |
| lignes 2 et 4 | la clôture prévue | la clôture prévue |
| ligne 6 | la clôture prévue | idem |
| ligne 7 | morceaux restants, sans clôture | idem |
| ligne 3, erreur de morceau | aucune commande | aucune commande |
| clôture déjà envoyée | attente de sa réponse, aucune commande ensuite | idem |

Sur le parcours `0xc9`, le nombre de commandes envoyées après l’annulation est borné par la borne de taille (au plus 10 morceaux de 432 octets, ou 3 de 1 984 octets, et une clôture). Sur le parcours `0xc8`, le pas vient du mot 6 et du `SenderSize` de la réponse ; l’exporter admet un pas d’un octet au moins, donc au plus 4 099 morceaux après l’ouverture, sans clôture, et ces morceaux restants passent par la fenêtre de 2 s sur les deux couples (sans clôture possible, s’arrêter en route laisserait le transfert à mi-chemin). Toute limite de temps porte sur ces **nouveaux envois** : une commande déjà envoyée n’est jamais interrompue et peut durer au-delà (section 6.4).

**Abandon sans clôture.** Vérifié dans le pilote Linux [public] : le pilote ne garde aucun état de transfert paginé ; chaque commande alloue un bloc de commande et le libère à son retour (sauf interruption par un signal, défaut documenté dans `docs/aacraid-defaut-pilote.md`, indépendant de ce choix). Un transfert abandonné ne reste donc ouvert que dans le firmware ; l’effet sur la carte n’est pas établi, et c’est ce que fait l’outil sur une erreur de statut ou d’écho. L’effet d’une clôture sans transfert correspondant (transfert inexistant, déjà clos ou appartenant à un autre outil) n’est pas établi non plus : l’exporter n’en émet jamais. Implémenté : le transport ne clôture pas après un échec d’échange en cours de transfert, comme l’outil ; depuis le 2026-11-27, il clôture après un écart que seul lui contrôle ou des données rejetées, cas où l’outil serait allé jusqu’à la clôture (analyse statique 2.05 et 3.07 : l’outil ne contrôle que le statut et l’écho d’un morceau) ; la clôture reprend l’identifiant de l’ouverture validée ; un arrêt constaté avant l’envoi du morceau suivant garde la clôture prévue après le dernier morceau conforme (annulation, ci-dessus) ; si ce morceau est déjà parti et échoue (échec d’échange), le transfert est abandonné sans clôture ; s’il est rejeté par un contrôle propre à l’exporter, ou si les données reçues sont rejetées, la clôture part (parcours `0xc9`), et son propre échec d’échange arrête la carte pour le cycle. Vérifié sur la file SCSI du pilote Linux au moment de l’implémentation.

**Données retenues.** Une clôture confirmée par `0xc6` confirme la fin du transfert, non les données : l’exporter ne les utilise que si le code d’ouverture est `0xda` et que les morceaux et le décodage sont valides.

**Codes d’ouverture par lecture.** Sur un transfert, l’outil :
- ne vérifie pas le code d’ouverture pour la liste et les informations des conteneurs (229, 234) : il utilise les données quel que soit le code, sauf `0xe2` ;
- exige `0xda` pour la liste et les informations des disques (247, 248) et pour la liste des membres (249) ;
- sur son chemin de collecte, n’accepte pas le code `0x11a` pour un disque : un disque répondu ainsi est ignoré (l’outil ne l’accepte que pour la relecture d’un disque isolé).

L’exporter exige `0xda` pour toutes ces lectures : identique à l’outil pour 247, 248 et 249, plus strict pour 229 et 234 (règle 3 : lecture en échec plutôt que des données sous un code inconnu).

#### Statut de preuve des parcours (S4)

- Nominal `0xc9` / `0xda` : capture (preuve a).
- Formes dérivées confirmées (preuve b), dans la portée observée : clôture immédiate sur `0xe2` après une ouverture `0xc9` (requête de clôture, 229) ou `0xc8` (247) ; clôture sur total nul (229) ; lecture complète puis clôture sur un code atypique (parcours complet, 248) ; parcours `0xc8` (morceaux 1 à 9 sans clôture, 247, pas de 432 octets) ; bit de poids fort du bit 13 (requêtes d’ouverture et de comptage) ; blocs de 2 048 octets (ioctl et tailles d’en-tête seulement).
- Reste à confirmer avant activation : transfert complet en blocs de 2 048 octets.

La matrice reprend cette portée par commande et par phase. Une forme n’est activée que par une entrée de la matrice qui fixe la commande, les capacités, le cas d’ouverture (ligne du tableau), la phase, la requête sérialisée, ses octets significatifs et les contrôles qui autorisent la transition ; une transition sans entrée active, après une ouverture déjà envoyée, est traitée comme un rejet (règle 2 : abandon, lecture en échec). Les tests vérifient les ioctl effectivement émis, y compris leur absence pour les combinaisons refusées, sur le modèle qualifié comme ailleurs. Les preuves (b) établissent les requêtes de l’outil face à des réponses construites pour déclencher chaque parcours : elles ne disent pas comment une carte réelle répond.

#### maxCache

Si `SupportedOptions2` porte le bit 17 (`0x20000`), l’outil lit après la 229 la liste des conteneurs maxCache (sous-commande `0xfe`, paramètre 13, taille demandée 512 octets ; réponse : identifiants terminés par zéro), puis l’information de chacun sur 432 octets, et l’ajoute aux volumes si cette lecture rend le code `0xda` et que l’octet 90 porte le bit `0x40`. Forme de `0xfe` confirmée ; lectures courtes de la 234 non confirmées : la découverte des conteneurs maxCache est hors de ce lot (point ouvert n° 4). Que la 229 contienne ou non ces conteneurs n’est pas établi.

#### Protection du cache par batterie

Réponse `0x30000` sans le bit `0x2000`. Décodage de l’outil, établi et confirmé en l’observant (versions 2.05 et 3.07). Positions relatives aux données de la réponse : mot d’indicateurs de 16 bits aux octets 42-43, qui dit quels champs sont présents ; octets d’état 20 (présence, indicateur `0x1`), 21 (batterie utilisable, `0x2`) et 22 (mode maintenance, `0x4`). Valeurs observées : 0 et 1 (indicateurs `0x17`).

| Octet 20 | Octet 21 | Octet 22 | `hwraid_cache_protection_present` | État publié |
|---|---|---|---|---|
| 0 | — | — | 0 | aucun (module non installé) |
| 1 | 0 | — | 1 | `failed` |
| 1 | 1 | 0 | 1 | `optimal` |
| 1 | 1 | 1 | 1 | `maintenance` (avertissement) |

- Un octet n’est lu que si son indicateur est présent ; « — » : octet non lu. Indicateur requis absent, ou valeur autre que 0 et 1 : état `unknown` ; la présence est publiée si l’octet 20 a été lu et vaut 0 ou 1, absente sinon. Dans tous ces cas, la lecture est un succès (`hwraid_collection_success` 1). Une réponse invalide (tronquée, statut ou écho faux) est une lecture en échec, sans état publié (« Résultat publié »). La recette couvre ces cas et les quatre cas observés.
- `maintenance` est une nouvelle valeur de `hwraid_cache_protection_state`, qui reprend le terme de l’outil. Ce n’est pas un état sain. À titre d’information, hors contrat : la documentation du constructeur pour ses générations précédentes décrit ce mode comme un cycle de reconditionnement de la batterie, de 8 à 10 heures, à autonomie réduite et cache en écriture directe ; l’état effectif du cache reste publié par volume (`hwraid_volume_write_cache_enabled`).
- Alertes : `HwraidCacheProtectionMaintenance` (avertissement, après 12 heures) ; `maintenance` s’ajoute aux états de `HwraidVolumeWriteCacheUnprotected` (cache d’écriture actif alors que la protection n’est pas garantie).
- Type publié : `battery`, `vendor_type` `bbu`.
- Les autres états affichés par l’outil (en charge, en décharge, charge désactivée) dépendent d’indicateurs que ce chemin ne fournit pas.
- La commande `0xa0000`, que l’outil envoie si l’indicateur `0x8` est présent, pour un champ complémentaire inutile à l’état, n’est pas envoyée par l’exporter.
- **Implémenté** (décodage, états, type, alertes), vérifié sur des réponses construites ; aucune batterie réelle lue par l’exporter.

#### Contrôle des formes (S2, S4)

- Seules partent les formes qui découlent des capacités de la carte destinataire, par phase du transfert (ouverture, morceau, clôture). Le contrôle est réparti ainsi (implémenté) :
  - le module est garant de la provenance : il lit la 706, la 703 et le comptage sur la carte, les garde sous l’identité complète de la carte (comme la 706), et n’émet le comptage et les lectures paginées que si ces capacités l’autorisent (bits 13 et 15 absents, bit 9 pour le comptage et les lectures de volumes) ;
  - le transport contrôle la forme au point d’envoi commun, y compris sur son API brute : la 229 n’y part qu’à la taille déduite d’un comptage décodé d’une réponse de la carte (type opaque, valeur nulle refusée), lié au descripteur ouvert sur cette carte ;
  - le transport ne relit pas lui-même les capacités : cela ajouterait des commandes à chaque cycle, contre la règle de conduite. Ce contrôle détecte une erreur de programmation de l’exporter ; module et transport sont dans le même processus, sans frontière de confiance entre eux.
- Les morceaux et la clôture restent rattachés à l’ouverture validée sur le même nœud (point 4 de la section 7.7). Les requêtes de morceau peuvent reprendre des mots de la réponse précédente (observé : un morceau reprend le code `0xdb`, le suivant `0xda`). Implémenté : le transport exige `0xda` au mot 4 et le total au mot 7 de chaque réponse d’ouverture et de morceau (valeurs des 315 ouvertures et 2 226 morceaux capturés pour les lectures 229, 234, 247, 248 et 249 ; d’autres valeurs n’apparaissent que sur des lectures que l’exporter n’envoie pas), sinon abandon : ses requêtes, à gabarit fixe, restent celles que l’outil construirait.
- Pour chaque phase, clôture immédiate comprise, la provenance de chaque champ (réponse précédente ou valeur construite) et ses bornes sont spécifiées.
- Un test refuse un identifiant sentinelle, un morceau hors séquence, un mot de réponse incohérent et une continuation sur un autre nœud.
- Toute nouvelle commande ou forme sérialisée (dont `CT_GET_CONTAINER_COUNT`, et les nouvelles formes d’ouverture, de morceau et de clôture) reçoit une justification S8 (rejeu par le noyau) avant activation ; chaque forme ajoutée reçoit sa référence S3, même derrière un ioctl déjà inscrit.
- Le modèle qualifié ne reçoit que les formes qui découlent de ses capacités (les formes actuelles) et celles du déroulement des transferts ci-dessus (point 19).

**Résultat publié.** Lecture tentée en échec (réponse invalide, `MaxContainers` hors bornes, transfert interrompu, code autre que `0xda`) : `hwraid_collection_success 0` pour les collecteurs qui en dépendent, aucun état publié. Capacité exigée absente : `hwraid_collector_supported 0`.

**Limite, à documenter.** Les formes 2 à 4 et la poursuite d’un transfert sur un code atypique ne sont jamais déclenchées par la carte de référence (bits 8 et 9 de `FeatureBits` présents, bit 13 de `FeatureBits` et bit 15 de `SupportedOptions` absents, ouvertures `0xc9` et `0xda`) : elles seront vérifiées par des tests sur données construites uniquement. Ces tests prouvent que l’exporter envoie ce que l’outil envoie dans chaque situation reproduite ; ils ne prouvent pas qu’une carte réelle réponde comme ces situations le supposent. Les contrôles de taille et de cohérence rejettent les incohérences qu’ils détectent (échec ou `unknown`), jamais pour publier un état inventé.

#### Points d’analyse à établir avant l’implémentation

Outil, versions 2.05 et 3.07 ; une forme dont le point n’est pas résolu reste refusée :
1. Bit 13 et blocs de 2 048 octets : valeurs que la carte place dans ses réponses d’ouverture et de morceau (l’outil reprend ces mots dans les requêtes suivantes ; l’exporter les vérifie) ; pas des morceaux de 2 048 octets, établi par l’analyse seule.
4. maxCache : lecture de l’information des conteneurs maxCache sur 432 octets, et seconde requête `0xfe` (16 octets) émise par un autre appelant.

Résolus : parcours `0xc8` avec un mot 6 nul (l’outil prend un pas d’un octet ; l’exporter refuse, 2026-11-26), taille demandée et non total au mot 7 de ses morceaux, préconditions d’émission de `0x30000` sur le chemin de consultation, début de l’ouverture de la carte (704, 701, 703, 706 sous bit 16 ; la suite de l’ouverture reste à établir), codes d’ouverture examinés par chaque lecture, code `0x11a`, décodage de la batterie classique, chemin des disques sans `FeatureBits` bit 9, mots des requêtes de morceau du parcours `0xc8` (morceaux 1 à n, taille `| 0x80000000`, sans clôture), clôture immédiate sur `0xe2` après une ouverture `0xc9`, lecture complète puis clôture sur un code atypique, ordre des lectures, abandon sans clôture sur erreur de morceau.

#### À spécifier avec l’implémentation

Avant d’activer chaque forme :
- champs repris d’une réponse validée et leurs bornes ;
- requêtes sérialisées de chaque transition, clôture immédiate sans morceau comprise ;
- forme sérialisée complète de `CT_GET_CONTAINER_COUNT` et de ses variantes (bit 13, bit 15, les deux), écho et taille minimale de sa réponse, justification des octets mis à zéro ;
- tailles de réponse admises par phase, rejet des réponses tronquées, longueur utile des données de l’ouverture `0xc8`, pas nul ;
- taille demandée comme borne plus serrée que 4 100 octets (E3), si les captures montrent que le total ne la dépasse jamais ;
- traitement de `EINTR` (S8, sans relance immédiate) dans l’abandon ; durée maximale admise pour les nouveaux envois après une annulation sur un autre couple (fixée : 2 s comptées depuis l’arrêt, c’est-à-dire l’échéance du cycle ou le constat du signal d’arrêt par le transport, vérifiées à l’admission sur le thread dédié ; au-delà, abandon sans clôture ; implémenté) ;
- états des disques publiés sans données de volumes, avec recette (bit 9 absent, `MaxContainers` nul ou invalide, échec d’une 234) ;
- prérequis de la 708 retenus par l’exporter, préconditions de `0x30000` ;
- masques de batterie autres que ceux du tableau, si une carte en présente ;
- sélecteur `serial:` quand la série d’une autre carte est illisible ;
- séquence attendue par cycle, réutilisation des réponses gardées comprise ;
- statut d’activation de chaque combinaison dans la matrice ;
- description des formes dans `docs/aacraid-interface.md` ;
- valeur `maintenance` de `hwraid_cache_protection_state` et alerte `HwraidCacheProtectionMaintenance` dans `docs/metrics.md` et `docs/alerts.md`, alertes Grafana de déploiement comprises.

#### Qualification par modèle

Un modèle est qualifié par ses quatre identifiants PCI (S4) ; la version de firmware n’est plus une condition de lecture. Les écarts E1 (accepté par le firmware de référence) et E2 (choix de l’exporter, en service sur ce couple) restent attachés au couple modèle–firmware où ils sont appliqués  ; la protection du cache est lue sur toute carte lue. La documentation liste les couples modèle et firmware effectivement testés (aujourd’hui : Adaptec 6805, 5.3-0[19204]), et le firmware reste publié par `hwraid_controller_info`. Implémenté : table des modèles qualifiés et niveaux de lecture dans `docs/aacraid-interface.md` (section 8, table vérifiée contre le code par un test), `docs/alerts.md`, `docs/guide-capture.md` ; le transport reçoit du module l’indication du couple validé et applique sinon la suite de l’outil (tous les morceaux, morceaux restants à l’arrêt, pas de relecture après `0xc5`).

### 6.12 Modèles non qualifiés (option `-allow-unqualified`)

**État : formes `aacraid` et `serial:`, et métrique, implémentées (sélecteur `serial:` : voir « Sélecteur `serial:` » ci-dessous).** Sans option, une carte `aacraid` dont le modèle n’est pas qualifié est inventoriée, son nœud vérifié, aucune commande firmware envoyée (seul `GET_HBA_INFO`, traité par le pilote, est lu), et tous ses collecteurs publiés non pris en charge (alerte `HwraidVolumesNotMonitored`). L’option permet de la lire avec le même code que le modèle qualifié, selon sa règle de conduite : suite de commandes de l’outil, sans les écarts réservés au couple de référence (E1, accepté par son firmware ; E2, choix de l’exporter), avec les écarts qui valent partout (E3, E4). Elle reste désactivée par défaut : une carte jamais testée reçoit des commandes que le constructeur ne documente pas, et ce choix revient à celui qui l’exploite.

**Option**, répétable, deux formes cumulables :
- `-allow-unqualified=aacraid` : toutes les cartes du module `aacraid` (pilote du même nom), présentes ou futures ;
- `-allow-unqualified=serial:<numéro>` : une carte `aacraid` précise, résolue avant chaque collecte sur l’inventaire du cycle (numéro lu à ce cycle, valide et unique ; un label `pci:` est refusé ; un numéro absent, conservé d’un cycle précédent ou dupliqué invalide ce sélecteur pour la carte, sans effet sur une autorisation `aacraid` indépendante). Ce sélecteur vaut pour la carte qui annonce ce numéro, quel que soit le module qui qualifie ses cartes et la prend en charge (aujourd’hui `aacraid` seul) : il désigne une carte, jamais un module entier. Un numéro de série présent mais illisible sur un hôte quelconque de l’inventaire, ou une carte dont les hôtes annoncent des numéros différents (chacun compte comme annoncé par elle), pourrait masquer un doublon : le premier suspend tous les sélecteurs pour le cycle, la seconde n’est jamais désignée. Une carte n’est pas remplacée à chaud : un changement d’identité à la même adresse pendant le fonctionnement est une anomalie, journalisée (champs changés, sans valeurs). Les journaux ne reproduisent pas le numéro (S9) : une valeur est désignée par son rang. Si la découverte du cycle est partielle ou en échec, l’unicité n’est pas établie et le sélecteur ne vaut pas ; l’autorisation porte sur la carte dont le nœud est vérifié à ce cycle. Le numéro qui autorise la collecte est celui que sysfs annonce à ce cycle (jamais un numéro conservé) ; le nœud est vérifié par `GET_HBA_INFO` avant toute commande, et le même descripteur, ouvert une fois par cycle, sert à toutes les commandes suivantes du cycle. Le pilote rattache un descripteur à la carte présente à son ouverture [public] : une carte installée ensuite n’est jamais atteinte par ce descripteur. Le retrait à chaud d’une carte pendant un cycle n’est pas géré par le pilote sur ce chemin (il le signale lui-même) : hors périmètre. La recette couvre inventaire partiel, doublon, remplacement de carte et label conservé, avec et sans autorisation `aacraid`.

Pas de forme « toutes » : l’acceptation nomme ce qu’elle vise, pour qu’un module futur ne soit jamais activé par effet de bord. Un nom de module inconnu fait échouer le démarrage ; une valeur `serial:` qui ne désigne aucune carte présente est journalisée.

**Sélecteur `serial:`.** Le numéro publié par sysfs (`serial_number`, label `serial:`) est construit par le pilote à son chargement [public] : `serial[0]` de la 703 sur six chiffres hexadécimaux, remplacé par `mfg_pcba_serial_no` de la 706 (12 octets) seulement si ce dernier se termine par ces chiffres ; vide si `serial[0]` vaut `0xBAD0`. Il décrit la carte à laquelle le pilote s’est rattaché, celle que désigne le nœud. Une confirmation par la 706 lue au cycle n’est pas possible : c’est une commande firmware, qu’une carte non désignée ne reçoit pas (verrou commun) ; elle ne confirmerait d’ailleurs le numéro que dans le second cas. L’unicité est établie par le cœur sur l’inventaire du cycle et transmise au module avec la carte. Recette : tests du sélecteur (numéro autre, doublon y compris sur une carte sans module, inventaire incomplet, numéro illisible sur une autre carte, numéro devenu illisible, valeurs invalides refusées sans rien autoriser, argument positionnel refusé), et de l’unicité.

**Effet.** Le verrou de qualification du module (une commande firmware n’est envoyée que si le modèle est qualifié) devient : modèle qualifié, ou carte désignée par l’option. Le reste de la collecte est celui de la section 6.11.

**Métrique.** `hwraid_controller_qualified{controller}` : 1 si le modèle est qualifié, 0 sinon, pour chaque carte d’un module qui qualifie ses cartes. Aucune alerte dédiée ; `HwraidVolumesNotMonitored` suit l’état du collecteur des volumes.

**Risques acceptés par l’opérateur**, documentés dans le README et `docs/deploiement.md` :
- des formes jamais observées sur matériel, vérifiées face à des réponses construites (section 6.11, « Limite ») ;
- une fréquence plus élevée que l’outil, qui ne lit la carte que lorsqu’on l’appelle (intervalle long recommandé, par exemple 5 minutes) ;
- une commande sans réponse n’est pas interrompue (défaut du pilote, `docs/aacraid-defaut-pilote.md`) : la carte n’est plus collectée jusqu’à la réponse ;
- la concurrence avec l’outil constructeur : le pilote sérialise chaque ioctl, pas un transfert ; les contrôles de transfert rejettent les réponses dont ils détectent l’incohérence (la cause exacte des clôtures `0xc5` n’est pas établie).

## 7. Premier lot de travail et recette

### 7.1 Environnement de référence

| Élément | Valeur |
|---|---|
| Système | Debian 11, OpenMediaVault, systemd 247 |
| Noyau | `5.10.0-46-amd64` (Debian 5.10.262-1) |
| Pilote | `aacraid` 1.2.1[50983]-custom |
| Carte | Adaptec 6805, firmware 5.3-0[19204] |
| Topologie | Un volume exposé sur le canal 0 (`sg0`/`sda`), huit disques SATA exposés sur le canal 1 (`sg1` à `sg8`) |
| Outils présents | maxView Storage Manager 2.05 (service CIM actif) et ARCCONF 2.05 sur l’hôte ; ARCCONF 2.05 dans le conteneur témoin `arccheck` |
| Contrainte | Hôte de production unique : aucune dégradation volontaire, première émission de chaque commande conforme à la section 4 (S4) |

### 7.2 Investigations prioritaires

**1. Détecter la 6805 et identifier un volume logique.** Relever l’identité de la carte découverte dans sysfs (section 3.2) et l’identifiant local du volume lu via le pilote. Ce couple suffit pour comparer son état à celui du même volume dans ARCCONF.

**2. Prouver le sens d’une lecture native avant d’en généraliser le décodage.** Pour le premier volume, conserver ensemble la version noyau/pilote/firmware, le résultat ARCCONF horodaté, l’identité de la carte et du volume, la capture de la commande émise par le témoin (section 4, S4) et la réponse native anonymisée. Pour chaque lecture autorisée, documenter les sélecteurs, longueurs de requête et réponse, ordre des octets, statut du firmware et champs effectivement comparés, puis l’inscrire dans la liste blanche et la matrice de qualification. Tester le décodage des réponses tronquées et des erreurs firmware hors matériel. Le même dossier est attendu séparément pour la présence du ZMM et son état. Si le témoin ne décrit qu’un volume optimal, la détection de dégradation reste à qualifier sur capture.

**3. Fixer le comportement quand l’échéance expire pendant un ioctl.** Une échéance Go avertit l’ordonnanceur. L’appel dans le pilote peut encore être en cours, ou attendre le mutex de la carte détenu par un autre processus. Sur transport simulé, bloquer la carte A pendant plusieurs intervalles, y compris dès sa première lecture. À l’échéance, publier l’échec de la fonction, garder le dernier succès inchangé s’il existe et laisser la carte B publier. Conserver la réservation de A jusqu’au retour réel de l’appel pour éviter l’accumulation de lectures. Écarter une réponse tardive du cycle expiré, puis reprendre à une échéance normale. Vérifier que les scrapes répondent et que le nombre de lectures en vol sur A reste à un. Mesurer sur la 6805 l’effet des signaux du runtime Go (préemption) sur les appels en cours. [Sémantique de context en Go](https://pkg.go.dev/context)

**Ordre de travail.** L’identification de la carte dans sysfs et les sorties de référence sur la 6805 constituent la première action sur matériel. La preuve du décodage d’un volume et le test de blocage bornent le premier prototype. Mesurer ensuite la durée et le nombre de requêtes pour l’inventaire, les volumes, les membres et le ZMM, avec et sans le service CIM de maxView actif, avant de fixer la cadence par défaut.

### 7.3 Jalons

**Jalon 1 — Preuve sur un volume.** Capturer l’état de référence pour chaque contrôleur du banc. L’exemple suppose que le conteneur existant s’appelle `arccheck` et montre la carte 1. Son numéro appartient à l’outil de référence.

```bash
docker exec arccheck arcconf GETCONFIG 1 AD
docker exec arccheck arcconf GETCONFIG 1 LD
docker exec arccheck arcconf GETCONFIG 1 PD
docker exec arccheck arcconf GETSTATUS 1
```

Comparer ces sorties au premier lecteur natif. Conserver les captures anonymisées avec les versions du noyau et du firmware. Attribuer des pseudonymes cohérents aux mêmes disques dans toutes les captures.

**Jalon 2 — Inventaire et ZMM.** Documenter chaque lecture et son décodeur. Ajouter des jeux de réponses pour module présent, absent, en état non optimal et lecture en échec. Garder une distinction explicite entre réponse matérielle enregistrée et donnée synthétique.

**Jalon 3 — Exporter et distribution.** Livrer le lecteur natif, les tests de décodage, de liste blanche et d’exposition, le binaire autonome et son empreinte, l’image, Compose, les unités systemd, le profil seccomp, la configuration de scrape et les règles d’alerte. Documenter la matrice de compatibilité et les permissions observées.

### 7.4 Cas de recette

| Cas de recette | Critère de réussite |
|---|---|
| Liste blanche | Test à base de propriétés de la section 4 (S2) sur au moins 10⁶ tirages et sur toutes les valeurs connues des sources (`FSACTL_*`, `TakeABreakPt`, commandes de configuration) : seules les entrées de la liste atteignent le faux appel système. La liste chargée est identique à la liste déclarée. |
| Isolement du transport | La CI échoue si un paquet hors transport importe `syscall` ou `golang.org/x/sys/unix` (test avec un paquet piégé). |
| Ouverture par pilote | Le transport `aacraid` ouvre en `O_RDONLY\|O_NOFOLLOW`. Le transport `hpsa`/`smartpqi` ouvert en `O_RDONLY` sur un `sg` réel ou simulé échoue en `EPERM` et le test le détecte. |
| Préparation des nœuds — cas nominaux | Avec un vrai `mknod` sur un tmpfs de test monté sans `nodev`, en root dans un environnement isolé : sans carte prise en charge, aucun nœud. Avec la 6805 (ou sa simulation sysfs), `aac<unique_id>` créé en 0600 pour l’UID 65534 avec le major de `/proc/devices`, et `st_rdev` vérifié par `stat`. Deux démarrages successifs laissent un état identique. |
| Préparation des nœuds — refus | Un nœud obsolète portant son schéma de nom est remplacé ou supprimé. Un fichier ordinaire, un lien symbolique, un nœud bloc ou un nom inconnu dans le répertoire fait échouer le démarrage et reste intact. Un lien symbolique à la place du répertoire fait échouer le démarrage. Un major non résolu par nom est refusé. Deux démarrages concurrents sont sérialisés par le verrou. Rien n’est modifié en dehors de `/dev/hwraid_exporter/`. |
| Montage `nodev` | Un nœud créé sur un tmpfs monté `nodev` ne s’ouvre pas : le test documente l’échec et justifie l’emplacement sous `/dev`. |
| Abandon des privilèges | Après la préparation, en natif comme en conteneur : `Uid` et `Gid` valent 65534 sur les quatre champs de `/proc/<pid>/status`. `CapEff`, `CapPrm`, `CapInh`, `CapAmb` et `CapBnd` ne contiennent que `CAP_SYS_RAWIO`. `NoNewPrivs` vaut 1. Une tentative réelle de `mknod` et une ouverture de `/dev/sda` (ou d’un nœud appartenant à root) échouent. Le port HTTP n’est ouvert qu’après cette vérification. |
| Abandon en échec | En simulant l’échec d’une étape (capacité `SETPCAP` absente, par exemple), l’exporter s’arrête avec un code non nul avant d’ouvrir le port HTTP et sans avoir émis de commande au firmware. |
| Relance après changement de nœud | Sur faux sysfs, changer le major de `aac` pendant l’exécution : l’exporter s’arrête avec son code dédié. Relancé, il recrée le nœud et reprend la collecte. Si le nœud est invalide dès la préparation, l’exporter ne s’arrête pas et publie `hwraid_management_node_valid 0`. |
| Conteneur sur OMV — modèle cible | Avec le fragment Compose de la section 6.5 sur l’hôte de référence : nœud créé, privilèges abandonnés (cas « Abandon des privilèges »), collecte réussie. Un redémarrage de l’hôte n’exige aucune action. |
| Conteneur sur OMV — repli | Si le cas précédent échoue, documenter le point en cause, puis valider le même scénario avec `privileged: true` : après l’abandon des privilèges, l’écriture dans `/sys/class/scsi_host/hostH/reset_host` et l’ouverture de `/dev/sda` échouent. |
| Nœud incohérent | Remplacer le nœud dédié par un nœud au mauvais minor : `hwraid_management_node_valid 0` pour cette carte, aucune lecture tentée (compteur du faux transport à 0), les autres cartes restent collectées. |
| Cohabitation avec maxView | Avec le service CIM actif, sur 1 heure : redémarrer `stor_cimserver` trois fois et lancer `arcconf GETCONFIG 1 AL` toutes les 30 s. Aucune modification de `/dev/hwraid_exporter/` (empreinte `stat` identique), taux de collectes réussies au moins 95 %, chaque échec étant un dépassement d’échéance journalisé. Les états des volumes et des disques publiés sont identiques à ceux d’une heure sans le service. |
| Hôte sans outil constructeur | Sur un hôte ou une image de test sans storman ni ARCCONF : la préparation crée les mêmes nœuds et l’exporter publie la même liste de séries (noms et labels) qu’en présence de maxView. |
| `userns-remap` | Avec le remappage activé, l’exporter démarre, publie `hwraid_collection_success 0` et journalise une erreur d’accès explicite (cause `EPERM`). |
| Interruption d’un appel | Sur faux appel système renvoyant `EINTR`, la lecture est un échec sans nouvel essai immédiat. |
| Hôte mixte, deux pilotes distincts et un volume 0 par contrôleur | Deux séries distinctes. Après redémarrage, le rattachement à la bonne carte reste vérifiable. |
| Deux cartes du même pilote | Inverser les sélecteurs locaux dans les fixtures (minor AAC, `host_no` MegaRAID ou nœuds SG). Vérifier que chaque volume reste rattaché à la carte attendue par son parent PCI et sa série. Refuser une correspondance ambiguë. |
| Accès Docker incomplet | Laisser sysfs visible et retirer le nœud de gestion ou la capacité. Une carte déjà identifiée reste inventoriée avec une collecte en échec. Au premier démarrage, vérifier le diagnostic PCI si la série reste indéterminée. |
| Identité entre redémarrages | Inverser l’ordre de découverte et redémarrer l’exporter puis recréer le conteneur. À matériel et topologie identiques, chaque carte garde exactement son label. Deux cartes de même modèle à série absente ou dupliquée restent distinctes. |
| Déplacement ou remplacement | Une carte avec série unique garde son identité après changement de slot. Un remplacement par une carte de série différente crée une nouvelle identité. En repli PCI, deux cartes successives au même emplacement peuvent partager la même série temporelle. |
| Une troisième carte inconnue | Présente dans l’inventaire, statut de prise en charge à 0, métriques RAID indisponibles ; les deux autres cartes restent collectées. |
| Plusieurs disques, dont un secours | Chaque disque et son rôle sont visibles. Un secours global peut exister sans relation à un volume. |
| Dégradation, membre manquant, puis reconstruction | Sur captures enregistrées ou banc dédié, jamais sur le NAS de production : le volume concerné quitte l’état optimal, la perte de membre reste visible, la progression publiée diffère de celle du témoin de moins de 2 points de pourcentage à lecture simultanée, et la série de tâche disparaît à la fin. |
| ZMM sur une seule des deux cartes | Présence et état rattachés à la bonne carte. L’absence confirmée, la charge, la panne et une erreur de lecture produisent les observations distinctes définies plus haut. |
| Réponse tronquée ou valeur inconnue | Chaque décodeur est testé sur des réponses tronquées et des valeurs inconnues (fuzzing Go natif prévu, section 7.7) : jamais de panique ni de faux état optimal. Une réponse tronquée échoue au décodage ; une valeur complète inconnue est conservée comme telle. |
| Progression invalide | `finalTick = 0` ou `currentTick > finalTick` : ratio omis, anomalie journalisée. Type de tâche inconnu : `task="other"`. |
| Une carte ne répond plus ou disparaît | L’autre carte continue d’être supervisée. Des valeurs anciennes ne prolongent pas artificiellement un état sain. |
| Volume sans correspondance Linux ou périphérique renommé | La mesure d’état reste publiée pour le même couple contrôleur–volume. Une association ambiguë est omise. |
| Disques masqués par le contrôleur | Les membres et leurs états RAID remontent via le module lorsqu’ils sont accessibles. L’absence de périphérique individuel ne génère aucune mesure SMART ni fausse panne disque. |
| Cadences mixtes (prévu, intervalle par contrôleur non implémenté) | Avec deux cartes simulées et des lectures instantanées, configurer 60 s globalement et 5 min pour une carte. De t=0 à t=300 s inclus, vérifier six départs sur la première et deux sur la seconde, premier cycle compris. Répéter avec scrapes concurrents et ordre de découverte inversé. |
| Collecte sans Prometheus | Avec une horloge de test, avancer de trois périodes sans appel HTTP. Les compteurs du faux pilote attestent des cycles prévus. |
| Scrapes en rafale | Figer l’horloge entre deux publications. Envoyer 100 requêtes concurrentes à `/metrics`. Toutes lisent les mêmes valeurs et les compteurs de découverte et de lecture matérielle restent inchangés. |
| Collecte bloquée et publication atomique | Bloquer la lecture d’une carte dans le faux pilote. Vérifier que l’autre carte progresse, que HTTP répond pendant le blocage et que chaque réponse correspond entièrement à une vue publiée. |
| Premier cycle et péremption | Vérifier la vue initiale, le premier échec sans valeur métier, puis un succès. Suspendre le producteur : les scrapes continuent de répondre et la date de dernier succès reste fixe. |
| Exposition HTTP | Seul `/metrics` répond (toute autre route : 404). |
| Binaire autonome et parité | Sur Linux sans Docker, l’unité systemd unique démarre le binaire, qui abandonne ses privilèges et expose `/metrics`. À version, paramètres, permissions et matériel identiques, comparer inventaire et états métier avec ceux de l’image. Les empreintes du binaire publié et de celui de l’image sont identiques. |
| Image de production | La CI vérifie que l’exécutable ELF Linux n’a ni interpréteur dynamique ni bibliothèque partagée requise. Le test HTTP interroge `/metrics` depuis l’extérieur du conteneur `scratch`. |
| Conteneur sur OMV — SMART | Aucune interrogation SMART dupliquée avec smartctl_exporter. |

### 7.5 Qualification et livraison

**Qualification multi-constructeurs.** Livrer la découverte générique et le contrat de module dès le premier jalon. Le test mixte peut d’abord être rejoué sur des données synthétiques. Pour annoncer deux familles compatibles en production, tester chacune sur matériel et vérifier leur coexistence sur hôte mixte. Un pilote ou un logo OEM commun ne suffit pas à étendre une qualification à tous les modèles.

**Condition de livraison du module AACRAID.** La première version qualifiée exige, sur une vraie 6805 et contre le témoin : l’inventaire du contrôleur, la liste des volumes, l’état de redondance de chaque volume, l’inventaire des disques avec leur appartenance, et la présence et l’état du ZMM. La perte de redondance, objet de la première question de la section 1, ne peut pas rester ouverte. Les états rares (dégradation, reconstruction, panne du ZMM) peuvent être développés sur captures puis éprouvés sur banc ou lors d’un événement réel. Marquer explicitement les scénarios simulés, observés sur matériel ou encore ouverts. Le NAS de production ne doit pas être volontairement dégradé pour fabriquer une capture de test.

### 7.6 Diagnostic facultatif du périphérique bloc associé au volume

Pour rechercher le nom courant du périphérique bloc, par exemple `/dev/sda`, partir du volume déjà identifié sur la carte, avec les attributs `level` et `unique_id` décrits en section 3.2. Le `unique_id` du périphérique SCSI et celui de `hostH` ont deux significations différentes. Écarter les identifiants nuls, vérifier les doublons par carte et conserver le numéro de volume firmware pour la comparaison avec ARCCONF. Le niveau RAID renseigne la configuration du volume. La perte de redondance exige une observation d’état distincte.

Relevé facultatif sur l’hôte, en lecture seule. Les numéros H:C:T:L désignent l’hôte, le canal, la cible et le LUN.

```sh
uname -r
for host in /sys/class/scsi_host/host*; do
    [ "$(cat "$host/proc_name" 2>/dev/null)" = aacraid ] || continue
    printf 'Hôte %s → %s\n' "${host##*/}" "$(readlink -f "$host/device")"
    for attr in unique_id model hba_kernel_version serial_number; do
        [ -r "$host/$attr" ] && printf '  %s=%s\n' "$attr" "$(cat "$host/$attr")"
    done
    number=${host##*host}
    for entry in /sys/class/scsi_device/"$number":*; do
        [ -d "$entry/device" ] || continue
        printf '  SCSI %s\n' "${entry##*/}"
        for block in "$entry"/device/block/*; do
            [ -d "$block" ] && printf '    périphérique bloc=/dev/%s\n' "${block##*/}"
        done
        for attr in level unique_id; do
            [ -r "$entry/device/$attr" ] && printf '    %s=%s\n' "$attr" "$(cat "$entry/device/$attr")"
        done
    done
done
```

### 7.7 Travaux identifiés par la revue du 2026-11-23

Deux revues indépendantes du dépôt ont été menées. Les corrections immédiates sont faites ; restent à traiter :

1. **Filtre seccomp.** En conteneur : profil `deploy/docker/seccomp.json` (profil par défaut de Docker moins `iopl` et `ioperm`), vérifié en CI. En natif : `SystemCallFilter` de l'unité systemd. Un filtre posé par le processus lui-même reste possible, pour une protection indépendante du mode de déploiement.
2. **Contrat de module pour les pilotes suivants.** Nœud partagé entre cartes (`megaraid_sas_ioctl_node`), descripteur ouvert pendant la préparation puis transmis après l'abandon de `CAP_SYS_ADMIN`, association des nœuds `sg` à leur carte (`hpsa`, `smartpqi`), échéance passée à `Collect`, capacités conservées seulement pour un plan valide.
3. **Sens des dates de dernier succès pour une valeur gardée en cache** : fait pour les capacités (aucune date tant que la réponse est gardée ; datée quand l'identité incomplète impose une relecture à chaque cycle, voir `docs/metrics.md`) ; à étendre aux valeurs gardées des pilotes suivants.
4. **Lier les morceaux et la clôture d'un transfert paginé à son ouverture** : fait dans le transport (identifiant, total, numéro, marque et taille de réponse ; aucune clôture après une ouverture non conforme). Reste à le vérifier dans la liste blanche elle-même.
5. **Bits du mot d'état ZMM.** Fait le 2026-11-27 : la règle plus stricte que l'outil (tout bit non qualifié publié `unknown`) a publié `unknown` pour le module neuf en charge, puis chargé (R = `0x4000008b`, bits `0x2` et `0x80`), que l'outil affichait « Charging / Not Ready » puis « Optimal ». L'analyse de l'outil a montré qu'il teste aussi le bit `0x2` (absent : « Charging / Not Ready ») et que `0x1` absent donne « not installed » ; la règle de l'exporter est désormais la sienne (docs/aacraid-interface.md, section 6). Reste : capturer un mot « Charging / Not Ready ».
6. **Vocabulaires des pilotes suivants.** Identifiant de disque `enclosure:slot`, états `predictive_failure`, `offline`, `unconfigured_bad` pour les disques, `learning` et `degraded` pour les batteries.
7. **Chaîne d'outillage.** Fait : actions et images de la CI et de la publication épinglées par empreinte. Reste à les tenir à jour (Dependabot).
8. **Rattachement du nœud au domaine PCI.** Fermé. Les quatre identifiants PCI sont comparés avant toute commande firmware ; le domaine PCI, absent de la réponse du pilote, n'est pas nécessaire : le numéro de carte comparé (`unique_id`, minor du nœud) est attribué par le pilote et unique sur la machine, quel que soit le domaine.
9. **Collecte hors délai interrompue entre deux commandes.** Fait : un contexte terminé à l'échéance du cycle ou à l'arrêt du service est transmis au module et au transport, qui n'envoient plus de commande firmware ensuite (ni nouvelle ouverture, ni relecture ; aucun morceau hormis ceux de la suite de fin d'un transfert ouvert) ; une commande en cours n'est jamais interrompue et un transfert paginé ouvert est terminé (aujourd'hui : morceau 0 puis clôture ; cible : suite par phase de la section 6.11, « Annulation »).
10. **Provenance des nœuds du répertoire dédié.** Non retenu : `/dev/hwraid_exporter` n'est modifiable que par root (et, en conteneur, n'existe que dans le `/dev` privé du conteneur). Un processus root qui y créerait un nœud dispose déjà de tous les droits ; la garde ne protégerait de rien.
11. **Mode `--dry-run`, niveau de journalisation et fuzzing des décodeurs**, annoncés dans les exigences de diagnostic et de recette.
12. **Dates de succès et caches liés à la carte, non à l'emplacement.** Fait : l'historique par carte et les réponses gardées sont rattachés à l'identité matérielle (`module.Card.Identity`) ; contrat dans `docs/metrics.md`.
13. **Découverte : attributs obligatoires et facultatifs.** Fait : `class`, `vendor`, `device`, `subsystem_vendor`, `subsystem_device` absents, illisibles ou vides et lien `driver` illisible sont des erreurs d'hôte ; un lien `driver` absent signifie « aucun pilote ».
14. **Masque de capacités calculé sur les cartes préparées.** Fait : seules les capacités des modules dont une carte a tous ses nœuds préparés sont conservées. Reste la recette des profils livrés (image, unité systemd) avec une carte sur la cible documentée.
15. **Intervalle de collecte par contrôleur** (section 6.8) : seul l'intervalle global existe ; à implémenter si une carte demande une cadence différente des autres.
16. **Température du contrôleur.** Fait en v0.1.3 (collecteur `temperature`, alertes à 55 et 60 °C et au seuil haut de la carte). Constat d'origine : non publiée, les outils de surveillance fondés sur ARCCONF (par exemple `check_adaptec_raid`, seuils d'avertissement et critique) la surveillent, et un exporter qui les remplace doit la couvrir. Établi le 2026-11-24 [interopérabilité] : commande publique `AdapterEnvironmentInfo` (803, FreeBSD `aacraid_reg.h`), un seul bloc, mot de données 0 = 1 ; réponse en champs de 16 bits : température en °C à la position 36, seuils de la carte à 40 (95 sur la 6805) et 42 (100) ; l'outil de référence (versions 2.05 et 3.07) affiche un état anormal quand la température atteint le seuil de la position 42. Concorde avec l'affichage de l'outil sur l'hôte de référence (48 et 49 °C). Condition d'envoi, comme l'outil de référence [public] : bit `AAC_SUPPORTED_HEAT_SENSOR` (0x80000) de `SupportedOptions`, mot à la position 80 de la réponse à `RequestAdapterInfo` (703, FreeBSD `aacraid_reg.h`) ; positionné sur la 6805 de référence.
17. **Épuisement de la réserve de commandes de gestion du pilote.** Le pilote n'expose aucun compteur de blocs libres (réserve de 8 par carte) : l'occupation n'est pas mesurable sans sonde noyau. Publier un compteur par contrôleur des refus `ENOMEM` sur les commandes de gestion, distinguant si possible les petites commandes (réserve) des grandes (tampon DMA), avec une alerte dédiée : l'épuisement est aujourd'hui visible au journal et par des lectures en échec seulement, alors qu'il change la conduite à tenir avant un redémarrage (cache non vidé, `docs/aacraid-defaut-pilote.md`).
18. **Généralisation des échanges et modèles non qualifiés** (sections 6.11 et 6.12, exigences S4 et S4 bis) : en cours. Implémentés : comptage des conteneurs et lectures selon les capacités, abandon sans clôture et identifiant sentinelle (point 19), qualification par modèle, suite de l’outil hors du couple validé, option `-allow-unqualified` (formes `aacraid` et `serial:`) et `hwraid_controller_qualified`. Implémentés aussi (2026-11-26) : lignes 2, 4, 6 et 7 du déroulement, formes 2 (bit 13) et 4 (parcours `0xc8`). Reste : forme 3 (blocs de 2 048 octets), non implémentée faute de format de réponse établi (champ `Size`, pas de 1 984 octets) ; à reprendre après une analyse statique ciblée et une capture d’une carte qui l’annonce. Ordre : généralisation (sur le scénario nominal capturé, requêtes du modèle qualifié inchangées à l’ajout attendu de `CT_GET_CONTAINER_COUNT` près, à vérifier par les tests de non-régression, sur deux cycles, avec et sans réponses gardées ; changements du point 19 testés séparément), puis qualification par modèle, puis option. Une forme n’est activée qu’une fois résolus ses points d’analyse et spécifiés ses points d’implémentation.
19. **Codes d’ouverture d’un transfert, modèle qualifié compris.** Sur une ouverture au statut et à l’écho corrects, avec un identifiant de transfert valide, l’exporter n’envoyait aucune commande si le code n’était pas `0xda` ou si le total était nul ; l’outil témoin clôture immédiatement sur le code `0xe2` (marque `0xc8` ou `0xc9`), et, sur marque `0xc9`, clôture sans morceau un total nul ou lit les morceaux puis clôture pour tout autre code, si bien qu’un transfert qu’il aurait fermé peut rester ouvert. Jamais observé sur l’hôte de référence (315 ouvertures capturées, toutes `0xda`, total non nul et dans la borne). Traité par la section 6.11 ; la clôture immédiate et la poursuite sur code atypique sont spécifiées et qualifiées séparément, avec la clôture sur total nul (les formes actuelles de morceau et de clôture portent `0xda` et un total non nul). Relevait aussi de ce point la clôture que le transport envoyait après toute erreur ou tout rejet en cours de transfert, y compris le rejet d’une liste des conteneurs qui annonce plus d’entrées que `MaxContainers` : elle a été remplacée par l’abandon sans clôture, puis, le 2026-11-27, rétablie pour les seuls rejets propres à l’exporter, l’analyse de l’outil ayant montré qu’il ne contrôle que le statut et l’écho d’un morceau et va donc jusqu’à la clôture dans ces cas (section 6.11, « Abandon sans clôture ») ; un échec d’échange reste sans clôture. État : abandon sans clôture et refus de l’identifiant `0xffffffff` implémentés ; clôture immédiate sur `0xe2` (marques `0xc9` et `0xc8`), clôture sur total nul et, à la place de la poursuite sur code atypique, clôture sans morceau, implémentées (2026-11-26, section 6.11, lignes 2, 4 et 6) ; restent sans commande la ligne 3 (E3), la ligne 7 (forme 4) et une taille différente du total.
20. **Suite de l’ouverture de la carte par l’outil (après la 706).** L’outil y lit des données que l’exporter n’exploite pas aujourd’hui : fonctions sous licence (905), commande 707, identifiant constructeur, informations du bus, état divers de la carte, paramètres des conteneurs, options RAID 6 ; il y lit aussi l’état de l’alarme sonore (190). Décision du 2026-11-26 : ne pas les envoyer tant qu’aucune donnée n’est utile ; leur échec, qui fait abandonner l’ouverture par l’outil, n’est donc pas reproduit (reste documenté, section 6.11). À reprendre dès qu’une de ces données devient utile (par exemple les licences), avec sa qualification S4 et l’analyse des sorties de l’ouverture qu’elle entraîne (matrice des commandes, T13). **Alarme sonore (2026-11-27) :** la 190 est désormais envoyée, mais seulement à la place de son second envoi par l’outil (après la configuration, section 6.11) ; son envoi d’ouverture ne l’est pas, et son échec à cet endroit, qui ferait abandonner la carte à l’outil, reste dans ce reste documenté (T13).
