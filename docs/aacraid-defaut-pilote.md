# Défaut du pilote aacraid : blocs de commande perdus

Le pilote Linux `aacraid` perd un bloc de commande de gestion chaque fois qu'un signal interrompt un programme qui attend la réponse de la carte. Les blocs perdus ne reviennent qu'au redémarrage de la machine. Quand il n'en reste plus, aucun outil ne peut plus gérer la carte, et le pilote ne peut plus demander à la carte de vider son cache à l'arrêt.

Cette page décrit le défaut, ses conséquences, ce que fait l'exporter pour ne pas le déclencher et comment détecter une fuite. Les règles d'exploitation qui en découlent sont dans le [guide de déploiement](deploiement.md#limite-du-pilote-aacraid--ne-pas-interrompre-une-commande).

## Symptômes

- L'exporter journalise `cannot allocate memory` avec `driver management command pool probably exhausted`, et `hwraid_collection_success` passe à 0.
- ARCCONF ne trouve plus la carte ; maxView, qui passe par la même réserve, ne peut plus l'interroger non plus.
- Les entrées-sorties disque continuent normalement : les volumes restent accessibles.

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
- pour un programme Go, les signaux que le runtime s'envoie lui-même (`SIGURG`, préemption asynchrone). On a vérifié localement qu'ils interrompent un appel système bloqué dans un autre thread. Jusqu'à la v0.1.1, l'exporter pouvait donc perdre des blocs sans qu'aucun arrêt n'ait lieu.

## Conséquence à l'arrêt : cache non vidé

À l'arrêt de la machine, le pilote envoie à la carte une commande qui vide son cache d'écriture et ferme les volumes (`VM_CloseAll`, `aac_send_shutdown` dans `comminit.c`). Cette commande prend son bloc **dans la même réserve**. Quand la réserve est vide, elle n'est pas envoyée, et aucun message ne le signale.

Avec le réglage par défaut du pilote (`cache=2`, `aachba.c`), Linux ne demande jamais non plus ce vidage. Le pilote annonce un volume sans cache d'écriture (WCE à 0) et ignore les demandes de vidage SCSI. Ni `fsync()` ni l'arrêt ne vident donc le cache de la carte.

Si le cache d'écriture de la carte est actif sans protection (module ZMM ou batterie en panne, ou cache forcé en écriture différée), un redémarrage avec la réserve vide peut perdre les dernières écritures. C'est arrivé sur l'hôte de référence le 2026-11-23 : des fichiers écrits dans les secondes qui ont précédé le redémarrage ont été retrouvés corrompus. Avant de redémarrer une machine dont la réserve est vide :

1. arrêter les applications qui écrivent ;
2. lancer `sync` ;
3. attendre une minute, le temps que la carte vide son cache d'elle-même.

## Protections de l'exporter

Depuis la v0.1.2 :

| Protection | Effet |
|---|---|
| Thread dédié aux commandes | Les commandes partent d'un thread système réservé qui bloque tous les signaux asynchrones. Le noyau livre à un autre thread les signaux adressés au processus (`SIGTERM`, `SIGINT`). Ceux qui visent ce thread (`SIGURG` du runtime Go) attendent la fin de la commande. Seuls les signaux de faute (`SIGSEGV`, `SIGBUS`, etc.) restent débloqués. Si le masquage échoue, aucune commande n'est envoyée. |
| Arrêt temporisé | Sur `SIGTERM`, l'exporter ne lance plus de cycle et attend jusqu'à 20 s la fin des commandes en cours avant de sortir. L'unité systemd donne 30 s (`TimeoutStopSec`) ; avec Docker, réglez `stop_grace_period: 30s`. |
| Délai de cycle | Le délai d'un cycle n'interrompt jamais une commande en cours : il empêche seulement d'en lancer de nouvelles. |
| Diagnostic | Un `ENOMEM` sur une commande de gestion est signalé comme une réserve probablement épuisée, réparable seulement par un redémarrage. |

**Ce que l'exporter ne protège pas :**
- un `SIGKILL` (`docker kill`, `docker rm -f`, fin du délai de grâce) ;
- les blocs perdus par les autres outils.

## Détecter une fuite et son auteur

[`tools/aacraid-fib-trace.sh`](../tools/aacraid-fib-trace.sh) pose une sonde du noyau sur le retour de `aac_fib_send`. Il n'y a rien à installer ; il faut être root. Pour chaque commande de gestion, la sonde note le programme qui l'a envoyée et le code rendu.

```sh
sudo tools/aacraid-fib-trace.sh            # Ctrl+C pour arrêter ; bilan par programme
```

- `ret=0` : la commande s'est terminée normalement.
- `ret=-512` : la commande a été interrompue, et un bloc est perdu. La ligne donne le nom et le PID du programme.

La sonde coûte peu (un événement par commande de gestion) et peut tourner plusieurs jours. Elle disparaît au redémarrage.

## Réglage `cache` du pilote

Le paramètre `cache` du module est un champ de bits. Il décide si Linux demande à la carte de vider son cache. Le vidage SCSI utilise le bloc propre à chaque entrée-sortie, **pas la réserve de gestion** : il fonctionne même quand la réserve est vide.

| Valeur | Effet |
|---|---|
| `2` (défaut) | Aucun cache annoncé, demandes de vidage ignorées. |
| `0` | Cache annoncé. Linux demande un vidage à chaque `fsync` et à l'arrêt. |
| `7` | Comme `0`, mais le pilote ignore les vidages quand il sait le cache protégé par la batterie ou le ZMM. Il ne le sait que s'il reçoit de la carte un événement batterie précis ; sinon `7` se comporte comme `0`. |

Avec `0` ou `7`, les programmes qui appellent souvent `fsync` (bases de données) vident le cache de la carte à chaque appel. Quand le cache est protégé, cela coûte en performance. Le paramètre se modifie à chaud dans `/sys/module/aacraid/parameters/cache`. Le volume doit ensuite être relu (`echo 1 > /sys/block/sdX/device/rescan`). Pour qu'il survive au redémarrage, il faut un fichier dans `/etc/modprobe.d/`, puis reconstruire l'initramfs. L'exporter ne modifie jamais ce réglage.

## Validation

- **Tests unitaires** (`internal/transport/aacio/thread_test.go`) : un appel système bloquant, émis depuis le thread dédié, n'est pas interrompu par `SIGURG`, `SIGTERM` ni `SIGUSR1`. Sans le masque, le même test échoue avec `EINTR`. L'arrêt attend la fin de la collecte en cours (`TestRunnerDrain`, `TestWaitIdle`).
- **Hôte de référence**, Adaptec 6805, Linux 5.10, le 2026-11-24, avec la sonde active :
  - pendant 30 minutes d'exporter, 1 924 commandes, toutes rendues normalement, sans aucune perte, y compris pour ARCCONF et maxView ;
  - arrêt par `docker stop` en 0,6 s avec le code 0.
- **Limite** : le rythme de perte de la v0.1.1 sur cet hôte n'a jamais été mesuré. L'absence de perte sur 30 minutes est cohérente avec le correctif, mais ne suffit pas à le prouver.
