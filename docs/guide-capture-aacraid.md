# Annexe aacraid : capturer les échanges d'ARCCONF

Cette annexe donne les commandes du [guide de capture](guide-capture.md) pour une carte gérée par le pilote Linux `aacraid` (Adaptec Series 6, 7 et 8). Les règles communes du guide s'appliquent : consultation uniquement, bibliothèque protégée, répertoire réservé à root, transmission par un canal privé.

## 1. Cas propres à aacraid

| Situation | Signe dans les métriques de l'exporter | Ce que la capture apporte |
|---|---|---|
| Modèle non qualifié | `hwraid_controller_qualified 0` ; sans `-allow-unqualified`, tous les collecteurs à `hwraid_collector_supported 0` | les réponses de ce modèle, avant toute qualification |
| Forme de transfert non implémentée | `hwraid_collector_supported 0` pour un collecteur autre que `cache_protection`, sur une carte lue | la forme demandée par la carte (bit 13 de `FeatureBits`, bit 15 de `SupportedOptions` : [aacraid-interface.md](aacraid-interface.md), section 7) ; sans capture, elle ne peut pas être ajoutée |
| Module de protection du cache non décodé | `hwraid_collector_supported{collector="cache_protection"} 0` | le format de ce module |
| Lecture implémentée sans preuve matérielle | — | voir ci-dessous |

Lectures implémentées d'après l'outil de référence, mais jamais observées sur une carte réelle :

- l'avancement d'une tâche de fond (reconstruction, vérification, mise à zéro, migration…), sous-commande 103 ([aacraid-interface.md](aacraid-interface.md), section 5) ;
- la batterie classique (BBU), dans ses états `optimal`, `failed`, `maintenance` (section 6) ;
- le module ZMM en charge ou tout juste posé ;
- un volume dégradé, un disque en panne ou de secours.

## 2. Prérequis

- La carte, sous Linux x86-64 avec la glibc, et le pilote `aacraid` chargé.
- ARCCONF 2.x ou 3.x, obtenu auprès du constructeur pour cette carte et installé normalement (la 3.07 est, à notre connaissance, la dernière version qui gère ces cartes).
- `gcc` (sans droits particuliers) et `sudo` pour lancer ARCCONF.
- L'accord de l'exploitant du serveur.

Commandes de consultation utilisées : `GETVERSION`, `GETCONFIG`, `GETSTATUS` et `GETCONFIGJSON`. Vérifier la syntaxe avec l'aide de la version installée (`arcconf help`) : certaines versions n'ont pas `GETCONFIGJSON`.

**Arrêter l'exporter pendant la capture, s'il tourne sur ce serveur.** Le pilote sérialise chaque commande, pas une lecture en plusieurs morceaux : deux outils qui lisent en même temps peuvent faire rejeter une lecture d'ARCCONF, et la capture contient alors des erreurs absentes en fonctionnement normal. La supervision est interrompue le temps de la capture (quelques minutes) ; si ce n'est pas acceptable, garder l'exporter et le signaler dans le ticket.

## 3. Construire et installer fibcap

La bibliothèque [`fibcap`](../tools/fibcap) journalise les ioctl de gestion `aacraid` qu'ARCCONF envoie par la glibc. Les commandes au firmware (FIB) sont entièrement contenues dans le tampon de l'appel : elle les journalise avant et après l'appel, sur la plage que le pilote copie. Le format du journal est décrit en tête de `fibcap.c`.

Depuis la racine du dépôt, **sans** `sudo` :

```bash
sh tools/fibcap/test.sh && echo FIBCAP_OK
```

Le script compile `tools/fibcap/fibcap.so`, puis le vérifie sans carte. Il doit se terminer par `FIBCAP_OK`.

## 4. Capturer

Créer, en root, un répertoire de séance réservé à root, y copier la bibliothèque (root ne doit charger qu'un fichier que seul root peut modifier), et repérer le chemin d'ARCCONF tel que root le voit :

```bash
cap=$(sudo mktemp -d /root/fibcap.XXXXXX) && sudo install -o root -g root -m 0644 tools/fibcap/fibcap.so "$cap/fibcap.so" && arc=$(sudo sh -c 'command -v arcconf') && echo "$cap $arc"
```

Relever les versions :

```bash
sudo sh -c '{ uname -r; cat /sys/class/scsi_host/host*/hba_kernel_version /sys/class/scsi_host/host*/model 2>/dev/null; sha256sum "$1"; } > "$2/versions.txt"' sh "$arc" "$cap"
```

Lancer chaque commande avec `fibcap`, en remplaçant `1` par le numéro de la carte dans ARCCONF. Chaque commande a son propre journal ; la ligne affichée donne le code de retour d'ARCCONF et le nombre de commandes firmware réussies et journalisées :

```bash
for c in "GETVERSION" "GETCONFIG 1 AD" "GETCONFIG 1 LD" "GETCONFIG 1 PD" "GETCONFIG 1 AL" "GETSTATUS 1" "GETCONFIGJSON 1"; do n=$(echo "$c" | tr ' ' '-'); sudo sh -c 'HWRAID_FIBCAP_OUT="$1/fibcap-$2.log" LD_PRELOAD="$1/fibcap.so" "$3" $4 > "$1/arcconf-$2.out" 2>&1; echo "$2 : code $?, $(grep -c "phase=RSP .* ret=0 .* fib=" "$1/fibcap-$2.log") FIB réussies"' sh "$cap" "$n" "$arc" "$c"; done
```

Un code de retour non nul ou zéro FIB réussie signale une commande indisponible dans cette version d'ARCCONF ou une capture incomplète : le noter dans le ticket.

Copier enfin la séance vers le compte courant. Les fichiers copiés lui appartiennent :

```bash
mkdir -m 700 ~/fibcap-capture && sudo tar -C "$cap" --exclude=fibcap.so -cf - . | tar -xf - -C ~/fibcap-capture
```

Une fois la transmission terminée, supprimer les deux répertoires, bibliothèque comprise : `~/fibcap-capture` et le répertoire de séance sous `/root` (`sudo rm -r "$cap"`).

**ARCCONF dans un conteneur.** Le conteneur doit déjà permettre à ARCCONF d'accéder à la carte (nœud de gestion et `CAP_SYS_RAWIO`). Copier `fibcap.so` dans un répertoire du conteneur réservé à root (l'image doit utiliser la glibc), puis lancer la même boucle avec `docker exec` et un répertoire de séance propre au conteneur. La capture ne demande aucun droit supplémentaire.

**États non optimaux.** Refaire la capture dans chaque état utile (section 1), en notant l'état et l'heure. Pendant une tâche de fond, lancer la boucle deux fois, à quelques minutes d'intervalle : l'avancement affiché par ARCCONF, comparé aux compteurs de la réponse, valide le calcul de l'exporter. La seconde boucle demande un nouveau répertoire de séance, la bibliothèque refusant d'écrire dans un journal existant. Vérifier ensuite que l'avancement a bien été demandé ; un résultat nul signale qu'aucune tâche n'était listée au moment de la commande :

```bash
sudo grep -cE 'phase=REQ .* fib=.{8}bc02.{52}67000000' "$cap/fibcap-GETSTATUS-1.log"
```

**Ce que publie l'exporter.** S'il lit cette carte (modèle qualifié ou `-allow-unqualified`), relever juste avant de l'arrêter ses métriques pour cette carte et ses lignes `msg="read failed"` du journal : elles permettent de comparer, valeur par valeur, ce qu'il publie à ce qu'affiche ARCCONF.

```bash
curl -s http://localhost:9799/metrics | grep -E '^hwraid_(controller_qualified|collector_supported|collection_success|task_progress_ratio|cache_protection_state|volume_state|disk_state)' > ~/exporter-metrics.txt
```

## 5. Transmettre

Suivre la section 4 du [guide](guide-capture.md). Le ticket indique en plus la valeur de `hwraid_collector_supported`, la version d'ARCCONF, et si l'exporter a été arrêté pendant la capture. Les fichiers utiles sont `fibcap-*.log`, `arcconf-*.out`, `versions.txt` et, le cas échéant, `exporter-metrics.txt` et les lignes du journal de l'exporter.

**Faire qualifier un modèle.** Il faut les captures nominales, celles des états utiles, puis la recette sur ce matériel par la personne qui a fourni les captures (section 5 du [guide](guide-capture.md)). Le modèle et son firmware entrent alors dans la [table des modèles qualifiés](aacraid-interface.md#modèles-qualifiés). Un essai avec `-allow-unqualified` aide à repérer ce qui manque, mais n'en tient pas lieu.
