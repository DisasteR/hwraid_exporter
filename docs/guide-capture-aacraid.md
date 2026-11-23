# Guide : capturer les échanges d'une carte aacraid

Ce guide s'adresse à une personne qui dispose d'une carte gérée par le pilote Linux `aacraid` que l'exporter ne sait pas encore lire. Les captures qu'elle produit permettent d'étendre le module sans posséder la carte.

## 1. Quand une capture est utile

| Situation | Signe dans les métriques de l'exporter |
|---|---|
| La carte n'annonce pas l'interface des lectures d'état ([aacraid-interface.md](aacraid-interface.md), section 7) | `hwraid_collector_supported{collector="volumes"} 0` |
| La carte annonce l'interface, mais avec un modèle ou un firmware jamais qualifié | Le label `firmware` de `hwraid_controller_info` n'apparaît pas dans la matrice de compatibilité |
| Un état n'a jamais été observé : volume dégradé, reconstruction, disque de secours, module de protection du cache en panne | — |

## 2. Principe

L'outil de gestion du constructeur, ARCCONF, interroge la carte à travers le même pilote que l'exporter. La bibliothèque `fibcap` ([tools/fibcap](../tools/fibcap)), chargée avec `LD_PRELOAD`, journalise chaque commande envoyée et chaque réponse du firmware, sans rien modifier. En plaçant ce journal à côté de ce qu'ARCCONF affiche au même moment, on établit le sens de chaque champ.

La capture se limite à observer un programme pendant son utilisation normale. Au regard du droit d'auteur européen, la personne habilitée à utiliser ARCCONF peut le faire sans autorisation du titulaire, et une licence ne peut pas l'interdire ([base-legale-interoperabilite.md](base-legale-interoperabilite.md) : directive 2009/24/CE, art. 5 §3 et art. 8). Cela ne dispense pas des autres autorisations : la copie d'ARCCONF doit avoir été obtenue régulièrement, et l'exploitant du serveur doit avoir accepté la capture et la transmission des fichiers.

## 3. Prérequis

- La carte, sous Linux x86-64 avec la glibc, et le pilote `aacraid` chargé.
- ARCCONF, obtenu auprès du constructeur pour cette carte et installé normalement.
- `gcc` (sans droits particuliers) et `sudo` pour lancer ARCCONF.
- L'accord de l'exploitant du serveur.

**Ne lancer que des commandes de consultation.** Le guide n'utilise que `GETVERSION`, `GETCONFIG`, `GETSTATUS` et `GETCONFIGJSON`. Aucune commande qui modifie la carte (création, suppression, réglage du cache, tâche) n'est nécessaire. Vérifier la syntaxe avec l'aide de la version installée (`arcconf help`) : certaines versions n'ont pas `GETCONFIGJSON`.

## 4. Construire et vérifier fibcap

Depuis la racine du dépôt, **sans** `sudo` :

```bash
sh tools/fibcap/test.sh && echo FIBCAP_OK
```

Le script compile `tools/fibcap/fibcap.so`, puis le vérifie sans carte. Il doit se terminer par `FIBCAP_OK`.

Copier ensuite la bibliothèque dans un répertoire que seul root peut modifier, pour que la commande lancée en root ne charge pas un fichier modifiable par un autre utilisateur :

```bash
sudo install -o root -g root -m 0644 tools/fibcap/fibcap.so /usr/local/lib/fibcap.so
```

## 5. Capturer

Créer un répertoire privé pour cette séance et repérer le chemin d'ARCCONF tel que root le voit :

```bash
cap=$(mktemp -d) && chmod 700 "$cap" && arc=$(sudo sh -c 'command -v arcconf') && echo "$cap $arc"
```

Relever les versions :

```bash
{ uname -r; cat /sys/class/scsi_host/host*/hba_kernel_version /sys/class/scsi_host/host*/model 2>/dev/null; sudo sha256sum "$arc"; } > "$cap/versions.txt"
```

Puis lancer chaque commande avec `fibcap`. Remplacer `1` par le numéro de la carte dans ARCCONF. `fibcap` refuse d'écrire dans un fichier qui existe déjà ; chaque commande a donc son propre journal.

```bash
for c in "GETVERSION" "GETCONFIG 1 AD" "GETCONFIG 1 LD" "GETCONFIG 1 PD" "GETCONFIG 1 AL" "GETSTATUS 1" "GETCONFIGJSON 1"; do n=$(echo "$c" | tr ' ' '-'); sudo env HWRAID_FIBCAP_OUT="$cap/fibcap-$n.log" LD_PRELOAD=/usr/local/lib/fibcap.so "$arc" $c > "$cap/arcconf-$n.out" 2>&1; echo "$n : code $?, $(sudo cat "$cap/fibcap-$n.log" 2>/dev/null | grep -c phase=RSP) réponses"; done
```

Chaque ligne affichée doit indiquer un nombre de réponses non nul. Les journaux appartiennent à root, en mode 0600. Les rendre lisibles par l'utilisateur courant, fichier par fichier :

```bash
for f in "$cap"/fibcap-*.log; do sudo chown "$(id -u):$(id -g)" "$f"; done
```

**ARCCONF dans un conteneur.** Copier `fibcap.so` dans le conteneur (l'image doit utiliser la glibc), puis lancer la même boucle avec `docker exec`, les mêmes variables d'environnement et un répertoire de sortie propre au conteneur. La capture ne demande aucun droit supplémentaire au conteneur.

**États non optimaux.** Refaire la capture dans chaque état utile, en notant l'état et l'heure. Ne jamais dégrader volontairement un volume qui contient des données : utiliser un volume de test. Sur un incident réel, ne capturer qu'avec l'accord de l'exploitant, quand le système est stable.

## 6. Transmettre

**Les captures identifient le matériel et sa configuration** : numéros de série et WWN de la carte, des disques et du module de cache, mais aussi noms et identifiants des volumes, topologie et horodatages. Elles ne doivent pas être jointes à un ticket public.

1. Ouvrir un ticket qui donne le modèle, le firmware, la version d'ARCCONF et la valeur de `hwraid_collector_supported`, sans aucune capture.
2. Les mainteneurs indiquent quelles commandes et quels journaux sont utiles, puis proposent un canal privé, avec des destinataires nommés et une durée de conservation.
3. Seuls entrent dans le dépôt des jeux de test minimaux, dont les champs identifiants sont remplacés par des valeurs synthétiques.

## 7. Ce que deviennent les captures

Pour chaque lecture, les mainteneurs associent les octets de la réponse aux valeurs affichées par ARCCONF, puis :

- ajoutent la requête à la liste blanche si elle est une lecture, si sa partie significative est identique à la capture et si les autres octets sont démontrés résiduels (exigence S4 du cahier des charges) ;
- écrivent le décodage et ses tests à partir des réponses anonymisées ;
- complètent [aacraid-interface.md](aacraid-interface.md) et la matrice de compatibilité.

Une carte n'est déclarée prise en charge qu'après une recette sur ce matériel, faite par la personne qui a fourni les captures.
