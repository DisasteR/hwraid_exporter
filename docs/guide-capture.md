# Guide : capturer les échanges d'une carte RAID

Ce guide s'adresse à une personne qui dispose d'une carte RAID que l'exporter ne sait pas encore lire, ou pas complètement. Les captures qu'elle produit permettent d'étendre le module du pilote sans posséder la carte. La démarche est commune à tous les pilotes ; les commandes à lancer sont dans l'annexe du pilote.

| Pilote | Outil du constructeur | Annexe |
|---|---|---|
| `aacraid` (Adaptec Series 6, 7, 8) | ARCCONF 2.x | [guide-capture-aacraid.md](guide-capture-aacraid.md) |
| `megaraid_sas`, `hpsa`, `smartpqi` | StorCLI, ssacli, ARCCONF 4.x | Écrite avec le module du pilote |

## 1. Quand une capture est utile

| Situation | Signe dans les métriques de l'exporter |
|---|---|
| La carte n'est pas prise en charge pour une fonction | `hwraid_collector_supported{collector="…"} 0` |
| Le modèle ou le firmware n'a jamais été qualifié | Le couple des labels `model` et `firmware` de `hwraid_controller_info` est absent de la liste des couples qualifiés |
| Un état n'a jamais été observé : volume dégradé, reconstruction, disque de secours, protection du cache en panne | — |

## 2. Principe

L'outil du constructeur interroge la carte à travers le même pilote que l'exporter. Une bibliothèque chargée avec `LD_PRELOAD` journalise les commandes de gestion qu'il envoie au pilote par la bibliothèque C, et leurs réponses, sans rien modifier. En plaçant ce journal à côté de ce que l'outil affiche au même moment, on établit le sens de chaque champ, et l'exporter peut envoyer exactement les mêmes requêtes que l'outil (exigence S4 du cahier des charges).

La capture se limite à observer un programme pendant son utilisation normale. Au regard du droit d'auteur européen, la personne habilitée à utiliser l'outil peut le faire sans autorisation du titulaire, et une licence ne peut pas l'interdire ([base-legale-interoperabilite.md](base-legale-interoperabilite.md) : directive 2009/24/CE, art. 5 §3 et art. 8). Cela ne dispense pas des autres autorisations : la copie de l'outil doit avoir été obtenue régulièrement, et l'exploitant du serveur doit avoir accepté la capture et la transmission des fichiers.

## 3. Règles communes

- **Consultation uniquement.** Ne lancer que les commandes de lecture listées dans l'annexe. Aucune commande qui modifie la carte (création, suppression, réglage du cache, tâche, mise à jour) n'est nécessaire.
- **Bibliothèque protégée.** La compiler et la tester sans droits particuliers, puis la copier dans le répertoire de séance réservé à root : l'outil est lancé en root et ne doit pas charger un fichier modifiable par un autre utilisateur. Rien n'est installé durablement sur le système.
- **Répertoire réservé à root.** Écrire les journaux dans un répertoire de séance créé par root et réservé à root, puis en copier le contenu vers le compte courant à la fin. La bibliothèque refuse d'écrire dans un fichier qui existe déjà.
- **Outil désigné par son chemin absolu**, tel que root le voit, et son empreinte relevée avec les versions du noyau et du firmware.
- **Volumes de données intacts.** Ne jamais dégrader volontairement un volume qui contient des données : utiliser un volume de test. Sur un incident réel, ne capturer qu'avec l'accord de l'exploitant, quand le système est stable.
- **Vérifier chaque capture** : l'annexe indique comment contrôler que chaque commande a produit des réponses.

## 4. Transmettre

**Les captures identifient le matériel et sa configuration** : numéros de série et WWN de la carte, des disques et du module de protection du cache, mais aussi noms et identifiants des volumes, topologie et horodatages. Elles ne doivent pas être jointes à un ticket public.

1. Avant toute capture, ouvrir un ticket qui donne le pilote, le modèle, le firmware, la version de l'outil et les métriques concernées, sans aucune capture.
2. Les mainteneurs indiquent les seules commandes et les seuls fichiers utiles, puis proposent un canal privé, avec des destinataires nommés et une durée de conservation. Le contributeur vérifie qu'il peut transmettre ces fichiers (règles de son organisation, données personnelles éventuelles : noms de volumes, horodatages).
3. Seuls entrent dans le dépôt des jeux de test minimaux : valeurs synthétiques, ou octets de réponse réduits aux champs décodés, les autres étant mis à zéro.

## 5. Ce que deviennent les captures

Pour chaque lecture, les mainteneurs associent les octets de la réponse aux valeurs affichées par l'outil, puis :

- ajoutent la requête à la liste blanche du module si elle est une lecture, si sa partie significative est identique à la capture et si les autres octets sont démontrés résiduels (exigence S4) ;
- écrivent le décodage et ses tests à partir des réponses anonymisées ;
- complètent la description de l'interface du pilote et la liste des couples modèle et firmware qualifiés.

Une carte n'est déclarée prise en charge qu'après une recette sur ce matériel, faite par la personne qui a fourni les captures.
