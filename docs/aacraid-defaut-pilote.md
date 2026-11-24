# Défaut du pilote aacraid : blocs de commande perdus

Le pilote Linux `aacraid` perd un bloc de commande de gestion chaque fois qu'un signal interrompt un programme qui attend la réponse de la carte. Les blocs perdus ne reviennent qu'au redémarrage de la machine. Quand il n'en reste plus, aucun outil ne peut plus gérer la carte, et le pilote ne peut plus demander à la carte de vider son cache à l'arrêt.

Cette page décrit le défaut, ses conséquences, ce que fait l'exporter pour ne pas le déclencher et comment détecter une fuite. Les règles d'exploitation qui en découlent sont dans le [guide de déploiement](deploiement.md#limite-du-pilote-aacraid--ne-pas-interrompre-une-commande).

## Symptômes

- L'exporter journalise `cannot allocate memory` sur toutes ses lectures, avec `the driver refused a management command for lack of memory`, et `hwraid_collection_success` passe à 0.
- ARCCONF ne trouve plus la carte ; maxView, qui passe par la même réserve, ne peut plus l'interroger non plus.
- Les entrées-sorties disque continuent normalement : les volumes restent accessibles.

Le pilote rend aussi `ENOMEM` quand il ne peut pas allouer le tampon d'une commande plus grande qu'un bloc (`ioctl_send_fib`, `commctrl.c`). Cet échec dépend de la mémoire du noyau disponible à cet instant, ne touche que les grandes commandes, par exemple la carte des connecteurs (2 048 octets), et ne consomme pas la réserve. Un `ENOMEM` isolé sur une grande commande, alors que les autres lectures réussissent, n'est donc pas une réserve vide. Des `ENOMEM` sur toutes les lectures, et ARCCONF qui ne trouve plus la carte, le sont très probablement.

Seul un redémarrage de la machine reconstitue la réserve. Lisez la section « Conséquence à l'arrêt » avant de redémarrer.

## Mécanisme

Le code cité est celui du noyau Linux. Il a été lu dans les versions 5.10 (Debian 11) et 7.4-rc4, et le défaut est présent dans les deux.

1. **Réserve.** Les commandes de gestion envoyées par les programmes (ioctl `FSACTL_SEND_FIB` et `FSACTL_SEND_LARGE_FIB`) prennent un bloc de commande (FIB) dans une réserve de 8 par carte (`AAC_NUM_MGT_FIB`, `aacraid.h`). Cette réserve est partagée par tous les outils : l'exporter, ARCCONF, maxView. Les entrées-sorties disque ont leurs propres blocs.
2. **Interruption.** `aac_fib_send` (`commsup.c`) attend la réponse de la carte de façon interruptible. Si un signal arrive pendant l'attente, elle marque le bloc comme abandonné (`done = 2`) et rend `-ERESTARTSYS`. `ioctl_send_fib` (`commctrl.c`) ne rend alors pas le bloc à la réserve : `if (retval != -ERESTARTSYS) aac_fib_free(fibptr)`. C'est le traitement de la réponse tardive qui doit le libérer.
3. **Réponse tardive.** Sur les cartes qui passent par `aac_intr_normal` (`dpcsup.c`), dont la série 6 (interface `src.c`), un bloc abandonné est terminé (`aac_fib_complete`) mais jamais rendu (`aac_fib_free` absent). L'ancien chemin `aac_response_normal` le rendait. **Le bloc est perdu jusqu'au redémarrage.**
4. **Commande envoyée deux fois.** Si le signal a un gestionnaire installé avec `SA_RESTART`, ce qui est le cas des programmes Go, le noyau relance l'ioctl avec un nouveau bloc : la carte reçoit la même commande deux fois. [Supposition] Cela pourrait expliquer certaines clôtures de transfert `0xc5` observées ([interface aacraid](aacraid-interface.md), section 4).

### Ce qui déclenche une perte

Tout signal reçu par un programme pendant qu'une de ses commandes de gestion attend la carte :

- Ctrl+C sur ARCCONF, un `timeout` ou un `kill` autour d'ARCCONF ;
- `SIGKILL` : `docker kill`, `docker rm -f`, ou la fin du délai de grâce de `docker stop` ;
- l'arrêt d'un conteneur dont le processus principal sort aussitôt sur `SIGTERM` : le noyau tue alors tous les autres processus du conteneur, ARCCONF compris, quel que soit `stop_grace_period` ;
- pour un programme Go, les signaux que le runtime s'envoie lui-même (`SIGURG`, préemption asynchrone). On a vérifié localement qu'ils interrompent un appel système bloqué dans un autre thread. Jusqu'à la v0.1.1, l'exporter pouvait donc perdre des blocs sans qu'aucun arrêt n'ait lieu ;
- un programme tué alors qu'il attend son tour. Le pilote ne traite qu'une commande de gestion à la fois par carte (`ioctl_mutex`, `aac_do_ioctl`, `commctrl.c`). Un programme qui attend ce verrou ne peut pas être interrompu : une fois le verrou obtenu, il envoie sa commande, puis l'abandonne aussitôt, car le signal est déjà là. [Probable, déduit du code]

### Origine, état amont et reproduction

- **Origine.** En 2009, le commit `cacb6dc3d7fe` (« [SCSI] aacraid: fix File System going into read-only mode ») a introduit l'abandon `done = 2` et libérait le bloc dans `aac_intr_normal`. En 2016, le commit `6bf3b630d0a7` (« aacraid: SCSI blk tag support », Linux 4.6) a retiré ces libérations lors du passage des entrées-sorties aux étiquettes du bloc layer ; le cas d'un bloc de gestion abandonné a été perdu au passage. Depuis ce commit, la liste libre ne contient plus que les 8 blocs de gestion. Tous les noyaux à partir de 4.6 sont touchés.
- **Pilote du constructeur.** Le pilote hors arbre de Microchip 1.2.1-60001 (2021) libère le bloc au même endroit. Sa fonction `aac_fib_free` ne remet dans la liste libre que les blocs de gestion. [Certain : lu ; non testé sur une carte]
- **État amont.** Au 2026-11-24, aucun correctif n'existait dans le noyau 7.4-rc4 ni dans la file SCSI de 7.5, et aucun signalement de ce défaut n'avait été trouvé sur linux-scsi ni sur bugzilla.kernel.org. Un correctif a été proposé le même jour sur linux-scsi, en RFC (compilé, pas encore testé sur une carte) : « [PATCH RFC] scsi: aacraid: free aborted management FIBs on late completion », <https://lore.kernel.org/all/20261124110444.3485-1-benj.saiz@gmail.com/>. Les réponses et les versions suivantes apparaissent dans ce fil.
- **Reproduction**, sur l'hôte de référence (Adaptec 6805, Linux 5.10), le 2026-11-24. Des sondes du noyau étaient posées sur `aac_fib_send`, `aac_fib_complete` et `aac_fib_free`, puis ARCCONF a été tué pendant une commande :
  - avant : 1 000 commandes de gestion, toutes sur le même bloc ;
  - ARCCONF reçoit `-512` sur ce bloc ;
  - 1,9 ms plus tard, la réponse de la carte arrive en interruption : le bloc est terminé, mais jamais libéré ;
  - après : 812 commandes, toutes sur un autre bloc ; le bloc abandonné n'est plus jamais réutilisé.

  Ce test a coûté volontairement un bloc, qui n'est rendu qu'au redémarrage suivant.

## Conséquence à l'arrêt : cache non vidé

Linux dispose de deux chemins pour demander à la carte de vider son cache d'écriture. Ils ne dépendent pas des mêmes ressources :

| Chemin | Quand | Bloc utilisé | Avec la réserve vide |
|---|---|---|---|
| Commande de gestion `VM_CloseAll` (`aac_send_shutdown`, `comminit.c`) | à l'arrêt de la machine, quel que soit le réglage `cache` | réserve de gestion | n'est pas envoyée, sans message |
| Vidage SCSI `SYNCHRONIZE CACHE` (`aachba.c`) | quand Linux en adresse un au volume (par exemple lors d'un `fsync` ou à l'arrêt), seulement si le volume annonce un cache d'écriture, et selon le réglage `cache` (section suivante) | bloc propre à chaque entrée-sortie | peut être envoyé |

Ce que l'on sait, et ce que l'on ne sait pas :

- **Avec le réglage par défaut du pilote (`cache=2`) et la réserve vide, aucun des deux chemins ne demande de vidage à l'arrêt.** Le volume annonce n'avoir aucun cache d'écriture (WCE à 0) et le pilote ignore les demandes de vidage SCSI. Le réglage effectif se lit dans `/sys/module/aacraid/parameters/cache`.
- **Avec un autre réglage, le vidage SCSI est demandé, sans garantie.** Avec `cache=7`, le pilote répond « fait » sans rien envoyer à la carte s'il croit le cache protégé, et peut de même neutraliser les écritures forcées sur disque (FUA). Avec `cache=0`, la commande part, mais peut échouer. Le statut que Linux reçoit est celui que la carte déclare, pas une preuve que les données sont sur les disques. `/sys/block/sdX/device/scsi_disk/*/cache_type` dit ce que le volume annonce à Linux, pas ce que la carte a fait.
- **Aucun vidage ne se constate depuis Linux.** Ni le pilote ni la carte n'indiquent que le cache est vide.

Si le cache d'écriture de la carte est actif sans protection (module ZMM ou batterie en panne, ou cache forcé en écriture différée), un redémarrage avec la réserve vide peut perdre les dernières écritures. C'est arrivé sur l'hôte de référence le 2026-11-23 (`cache=2`) : des fichiers écrits dans les secondes qui ont précédé le redémarrage ont été retrouvés corrompus. L'alerte `HwraidVolumeWriteCacheUnprotected` ([alerts.md](alerts.md)) signale un cache d'écriture actif sans protection opérationnelle.

**La seule mesure fiable est préventive** : dès qu'une protection tombe en panne, et tant qu'ARCCONF peut encore parler à la carte, régler le cache d'écriture des volumes en mode « seulement si protégé » (`wbb`) ou le désactiver (`wt`), **puis vérifier que la carte l'a coupé** : « Write-cache status: Off » dans ARCCONF, ou `hwraid_volume_write_cache_enabled` à 0 sur une collecte commencée après le changement (attendre deux intervalles). Ce relevé confirme le mode d'écriture effectif : les nouvelles écritures ne sont plus mises en attente. Il ne prouve pas à lui seul que les écritures acceptées avant le changement sont sur les disques ; laisser un peu de temps à la carte avant de redémarrer. Cette séquence a été observée sur l'hôte de référence le 2026-11-24.

Une fois la réserve vide, plus aucun outil de gestion ne peut changer ce réglage. Les mesures suivantes **réduisent le risque sans l'éliminer** :

1. arrêter les applications qui écrivent ;
2. lancer `sync`, pour que Linux remette à la carte tout ce qu'il garde en mémoire (avec `cache=2`, cela ne vide pas le cache de la carte) ;
3. laisser du temps à la carte avant de redémarrer. Elle vide son cache d'elle-même au fil de l'eau, mais aucun délai n'est garanti et rien ne permet de constater la fin du vidage.

Passer le pilote à `cache=0` (section suivante) ferait demander un vidage SCSI à l'arrêt sans passer par la réserve. Cette piste découle du code du pilote ; elle n'a jamais été essayée sur une carte dont la réserve est vide, et son succès ne serait pas observable.

## Protections de l'exporter

Depuis la v0.1.2 :

| Protection | Effet |
|---|---|
| Thread dédié aux commandes | Les commandes partent d'un thread système réservé qui bloque tous les signaux asynchrones. Le noyau livre à un autre thread les signaux adressés au processus (`SIGTERM`, `SIGINT`). Ceux qui visent ce thread (`SIGURG` du runtime Go) attendent la fin de la commande. Seuls les signaux de faute (`SIGSEGV`, `SIGBUS`, etc.) restent débloqués. Si le masquage échoue, aucune commande n'est envoyée. |
| Arrêt sans interruption | Sur `SIGTERM`, `SIGINT` ou `SIGHUP`, l'exporter ne lance plus de cycle, et le cycle en cours n'envoie plus aucune nouvelle commande, y compris une relecture. L'arrêt est vérifié sur le thread dédié, juste avant chaque appel système : une commande refusée n'atteint jamais le noyau. Seule la commande déjà envoyée se termine ; une lecture paginée ouverte va au bout de sa séquence la plus courte (premier morceau, puis clôture) et n'est pas publiée. En général, l'arrêt prend moins d'une seconde (0,48 s mesurée sur l'hôte de référence). L'exporter attend sans limite propre, en le journalisant toutes les 10 s : seul le délai du superviseur (`TimeoutStopSec` de l'unité systemd, `stop_grace_period: 30s` avec Docker) l'abrège. Dès le début de l'arrêt, quelle qu'en soit la cause (signal ou changement de nœuds), `/metrics` ne sert plus de valeurs matérielles (`hwraid_snapshot_ready 0`) : une attente longue ne fait pas passer un état ancien pour un état courant. |
| Délai de cycle | Le délai d'un cycle n'interrompt jamais une commande en cours : il empêche seulement d'en lancer de nouvelles. |
| Diagnostic | Un `ENOMEM` sur une commande de gestion est journalisé avec ses deux causes possibles : réserve probablement épuisée, ou tampon d'une grande commande non alloué. |

**Ce que l'exporter ne protège pas :**
- un `SIGKILL` (`docker kill`, `docker rm -f`, fin du délai de grâce), ni un `SIGQUIT`, qui garde son rôle Go de diagnostic (piles et sortie immédiate) ;
- les blocs perdus par les autres outils.

**Décision : le délai d'arrêt du superviseur reste borné (30 s dans les profils livrés).** Une commande qui ne rend jamais la main serait tuée à 30 s, et son bloc perdu. Un arrêt sans limite (`TimeoutStopSec=infinity`) l'éviterait, mais une carte figée bloquerait alors `systemctl stop` et le redémarrage de toute la machine. Perdre un bloc est préféré à une machine qui ne redémarre plus. Allonger ce délai reste possible, pour qui accepte ce risque.

**Limite connue :** le test de bout en bout de l'arrêt assemble la boucle, l'ordonnanceur, le module et le transport comme `serve`, mais ne lance pas `serve` lui-même ; l'arrêt de `serve` a été vérifié sur l'hôte de référence.

## Détecter une fuite et son auteur

[`tools/aacraid-fib-trace.sh`](../tools/aacraid-fib-trace.sh) pose une sonde du noyau sur le retour de `aac_fib_send`. Il n'y a rien à installer ; il faut être root, et le noyau doit offrir tracefs et les sondes de retour (kretprobes), présents sur Debian 11. Pour chaque retour capté, la sonde note le programme qui s'exécutait et le code rendu. Le filtre écarte les entrées-sorties disque soumises normalement (`-115`) ; il garde les commandes de gestion, mais aussi les rares retours en erreur des entrées-sorties et les appels internes du pilote. Une perte n'est donc attribuée à un programme de gestion que sur une ligne `ret=-512` portant son nom. La trace grossit d'une ligne par retour capté (quelques dizaines d'octets).

```sh
sudo tools/aacraid-fib-trace.sh            # Ctrl+C pour arrêter ; bilan par programme
```

La trace va dans un répertoire neuf et privé créé sous `/var/tmp` (chemin affiché au démarrage, lisible par root seul) ; le script ne prend aucun argument, et la capture s'arrête à 100 Mio (`AACFIB_MAX_MB`), limite que le noyau impose au lecteur (`ulimit -f`). La sonde porte un nom propre à l'exécution. À l'arrêt, le script coupe la sonde, vide le tampon dans la trace, retire la sonde et vérifie le retrait (un état illisible compte comme un échec). Il affiche les retours que la sonde n'a pas saisis et les événements perdus par le tampon : une trace sans `ret=-512` ne prouve l'absence de perte que si le bilan dit « trace complète », et seulement pour les commandes revenues pendant que la sonde était active : une commande encore en vol à l'arrêt de la sonde n'est pas observée. Pour conclure sur une commande précise, attendre son retour avant d'arrêter la sonde.

- `ret=0` : la commande s'est terminée normalement.
- `ret=-512` : la commande a été interrompue. Sur les cartes dont les réponses passent par `aac_intr_normal` (série 6, interface `src.c`, noyau 4.6 et plus), un bloc est perdu ; sur l'ancien chemin `aac_response_normal`, le bloc est rendu à l'arrivée de la réponse. La ligne donne le nom et le PID du programme.

La sonde s'exécute à chaque retour de `aac_fib_send`, y compris pour chaque entrée-sortie disque. Le filtre n'écarte ces dernières qu'ensuite, et seules les commandes de gestion sont écrites. Son coût sous forte charge disque n'a pas été mesuré : sur une machine chargée, préférer des sessions courtes. Elle disparaît au redémarrage.

## Réglage `cache` du pilote

Le paramètre `cache` du module est un champ de bits. Il décide si le volume annonce un cache d'écriture, et donc si Linux demande à la carte de le vider par le chemin SCSI. Ce vidage utilise le bloc propre à chaque entrée-sortie, **pas la réserve de gestion** : il fonctionne même quand la réserve est vide. La commande de gestion `VM_CloseAll` de l'arrêt ne dépend pas de ce réglage.

| Valeur | Effet |
|---|---|
| `2` (défaut) | Aucun cache annoncé, demandes de vidage ignorées. |
| `0` | Cache annoncé. Le pilote transmet à la carte les demandes de vidage que Linux lui adresse (par exemple lors d'un `fsync` ou à l'arrêt), sans garantie qu'elles aboutissent. |
| `7` | Cache annoncé. Le pilote répond « fait » à une demande de vidage sans l'envoyer à la carte quand il croit le cache protégé par la batterie ou le ZMM. Il ne le croit que s'il a reçu de la carte un événement batterie précis ; sinon il envoie le vidage, comme avec `0`. |

Avec `0`, et avec `7` tant que le pilote ne croit pas le cache protégé, les programmes qui appellent souvent `fsync` (bases de données) peuvent faire envoyer une demande de vidage à la carte à chaque appel. Quand le cache est protégé, cela coûte en performance. Le paramètre se modifie à chaud dans `/sys/module/aacraid/parameters/cache`. Le volume doit ensuite être relu (`echo 1 > /sys/block/sdX/device/rescan`). Pour qu'il survive au redémarrage, il faut un fichier dans `/etc/modprobe.d/`, puis reconstruire l'initramfs. L'exporter ne modifie jamais ce réglage.

## Validation

- **Tests unitaires** :
  - `internal/transport/aacio/thread_test.go` : un appel système bloquant, émis depuis le thread dédié, n'est pas interrompu par `SIGURG`, `SIGTERM` ni `SIGUSR1`. Sans le masque, le même test échoue avec `EINTR`. Le thread système est détruit à la fermeture du nœud ;
  - `internal/transport/aacio/device_test.go` : une fois l'arrêt demandé, aucune requête n'atteint le noyau, ni relecture après `0xc5`, ni seconde lecture du module de protection ;
  - `internal/transport/aacio/shutdown_e2e_test.go`, avec la boucle, l'ordonnanceur, le module et le transport réels et un ioctl simulé : un vrai `SIGTERM`, reçu pendant qu'une commande firmware attend la carte, au cours du cycle ou après son échéance, laisse cette commande aller au bout, n'en laisse passer aucune autre, et l'attente d'arrêt dure jusqu'à sa fin ; un test témoin montre que, sans l'arrêt, d'autres commandes auraient suivi ;
  - `internal/lifecycle/loop_test.go` : aucun cycle ne démarre après l'arrêt, et l'attente d'arrêt n'a pas de limite propre.
- **Hôte de référence**, Adaptec 6805, Linux 5.10, le 2026-11-24, avec la sonde active :
  - pendant 30 minutes d'exporter, 1 924 commandes, toutes rendues normalement, sans aucune perte, y compris pour ARCCONF et maxView ;
  - arrêt par `docker stop` en 0,6 s avec le code 0 ;
  - le 2026-11-24, version candidate de la v0.1.2 : 20 cycles conformes, arrêt en 0,48 s avec le code 0, sonde retirée sans reste.
- **Limite** : le rythme de perte de la v0.1.1 sur cet hôte n'a jamais été mesuré. L'absence de perte sur 30 minutes est cohérente avec le correctif, mais ne suffit pas à le prouver.
