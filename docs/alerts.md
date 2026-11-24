# Règles d'alerte d'exemple

Fichier : [`deploy/prometheus/hwraid-exporter.rules.yml`](../deploy/prometheus/hwraid-exporter.rules.yml), testé par [`hwraid-exporter.rules.test.yml`](../deploy/prometheus/hwraid-exporter.rules.test.yml). Les métriques utilisées sont décrites dans [metrics.md](metrics.md).

Ces règles sont un point de départ : seuils, durées et gravités sont à adapter à votre exploitation.

## Principes

- **Un trou de lecture n'éteint pas une alerte, et ne réveille jamais une valeur périmée.** Quand une lecture échoue, l'exporter retire les séries au lieu de publier une valeur non observée. Les règles ne prennent que l'état publié ; une alerte déjà déclenchée reste active **10 minutes** après la disparition de sa condition (`keep_firing_for`, Prometheus 2.42 et plus, vmalert). Les 10 minutes se comptent à partir de la première évaluation de la règle où la condition est absente : si une lecture échoue puis qu'un état sain est publié quelques minutes plus tard, l'alerte s'éteint moins de 10 minutes après ce retour ; à l'inverse, le délai de collecte, l'intervalle de lecture de Prometheus et celui d'évaluation des règles s'ajoutent au trou. En contrepartie, une alerte s'éteint 10 minutes après la première évaluation où sa condition est absente, que ce soit un trou de lecture ou un retour à la normale ; elle peut donc s'éteindre moins de 10 minutes après le retour à la normale si un trou l'a précédé, et le délai `for` d'une alerte pas encore déclenchée repart de zéro après un trou. Une lecture qui échoue durablement est signalée par `HwraidCollectionStale`.
- **Joindre sur `job` et `instance`.** Deux hôtes peuvent publier le même identifiant de contrôleur (`pci:0000:01:00.0`) : toutes les jointures portent aussi `job` et `instance`.
- **La perte de visibilité est une alerte à part entière.** Une lecture qui échoue durablement, un exporter absent ou un nœud de gestion invalide empêchent les alertes d'état de se déclencher : ils sont signalés séparément.
- **`unknown` n'est pas une panne**, mais un état qui doit être compris (voir ci-dessous).

## Règles

| Alerte | Condition | Délai | Gravité | Action |
|---|---|---|---|---|
| `HwraidVolumeFailed` | volume `failed` | immédiat | critical | données inaccessibles ou perdues : restauration |
| `HwraidVolumeDegraded` | volume `degraded` | 5 min | critical | remplacer le disque en panne ; une seconde panne dans ce volume serait fatale |
| `HwraidVolumeRebuilding` | volume `degraded_rebuilding` | 5 min | warning | laisser la reconstruction aboutir, éviter toute coupure |
| `HwraidVolumeStateUnknown` | volume `unknown` | 30 min | warning | vérifier les volumes membres et l'outil du constructeur |
| `HwraidDiskFailed` | disque `failed` (membre absent de la carte) | immédiat | critical | remplacer le disque |
| `HwraidVolumeDisappeared` | volume vu dans les dernières 24 h, absent alors que la lecture réussit | immédiat, pendant 24 h | warning | suppression ou perte du volume ; mettre en silence après une suppression volontaire |
| `HwraidCacheProtectionFailed` | protection du cache `failed` | 5 min | warning | remplacer le module ; en attendant, régler le cache d'écriture en mode « seulement si protégé » (Adaptec : `SETCACHE … wbb`) |
| `HwraidCacheProtectionStateUnknown` | protection du cache `unknown` (combinaison d'état non qualifiée) | 30 min | warning | vérifier avec l'outil du constructeur ; sa protection n'est pas garantie |
| `HwraidCacheProtectionNotReady` | protection du cache `charging` | 2 h | warning | une charge normale dure quelques minutes |
| `HwraidVolumeWriteCacheUnprotected` | cache d'écriture actif (`hwraid_volume_write_cache_enabled` 1) alors que la protection est absente, `failed` ou `charging` | 5 min | critical | une coupure de courant, ou un redémarrage sans vidage du cache, perd les écritures en attente : régler le cache en mode « seulement si protégé » (`wbb`) ou le désactiver, puis vérifier que `hwraid_volume_write_cache_enabled` passe à 0 |
| `HwraidVolumeWriteCacheProtectionUnknown` | cache d'écriture actif alors que la protection est `unknown`, que la carte ne permet pas de la lire (`hwraid_collector_supported{collector="cache_protection"}` 0) ou que sa lecture échoue (`hwraid_collection_success{collector="cache_protection"}` 0) | 30 min | warning | protection non garantie : vérifier le module avec l'outil du constructeur |
| `HwraidVolumeWriteCacheAlwaysOn` | réglage `write_back_always` | 15 min | warning | risque latent : une panne du module laisserait le cache actif ; préférer `write_back_when_protected` |
| `HwraidVolumeWriteCacheInactive` | cache d'écriture inactif alors que le réglage le demande et que la protection est `optimal` | 2 h | info | écritures ralenties : la carte n'a pas réactivé le cache, vérifier avec l'outil du constructeur |
| `HwraidVolumeWriteCacheUnknown` | lecture des volumes réussie, mais réglage `unknown` ou état du cache non publié | 30 min | warning | selon le champ inconnu, certaines alertes de cache ne couvrent plus ce volume (voir « Cache d'écriture ») : vérifier avec l'outil du constructeur |
| `HwraidCollectionStale` | aucun succès depuis plus de 3 intervalles de collecte (lecture en échec ou hors délai) | 1 min | warning | consulter le journal de l'exporter ; `cannot allocate memory` sur toutes les lectures : réserve du pilote très probablement épuisée ; avec le réglage par défaut du pilote, un redémarrage ne videra pas le cache de la carte ([aacraid-defaut-pilote.md](aacraid-defaut-pilote.md)) |
| `HwraidCollectionIntermittent` | au moins six passages succès/échec d'une lecture en une heure (environ trois échecs entre des succès) | immédiat | warning | ces trous empêchent les alertes à délai de se déclencher : vérifier l'état avec l'outil du constructeur, consulter le journal |
| `HwraidCollectionNeverSucceeded` | lecture en échec, jamais réussie depuis le démarrage | 15 min | warning | consulter le journal de l'exporter ; même cas de `cannot allocate memory` |
| `HwraidCollectionIntervalTooLong` | intervalle de collecte supérieur à 5 min | immédiat | info | allonger les fenêtres des règles d'état |
| `HwraidManagementNodeInvalid` | nœud de gestion non préparé, non vérifié ou désignant une autre carte | 5 min | warning | erreur de déploiement, carte changée |
| `HwraidControllerUnsupported` | carte sans module | 1 h | info | carte non surveillée |
| `HwraidControllerDisappeared` | contrôleur vu dans les dernières 24 h, absent alors que la découverte réussit | immédiat, pendant 24 h | warning | carte retirée, pilote déchargé ou identité changée |
| `HwraidDiscoveryFailed` | découverte sysfs en échec (au moins un hôte SCSI illisible) | 15 min | warning | une carte peut ne plus être surveillée |
| `HwraidSnapshotNotReady` | exporter joignable mais aucune collecte servie (`hwraid_snapshot_ready` 0 : démarrage, arrêt en cours, vue périmée) | 10 min | warning | consulter le journal de l'exporter |
| `HwraidExporterDown` | instance ayant publié des métriques `hwraid_` dans les dernières 24 h, `up == 0` | 10 min | warning | exporter arrêté ou injoignable ; une cible retirée de la configuration de Prometheus n'a plus de série `up` et n'est pas vue |
| `HwraidExporterAbsent` | aucune métrique `hwraid_` sur l'ensemble des instances | 10 min | warning | filet global ; ne voit pas la perte d'une instance parmi plusieurs |

## Adapter les règles à l'intervalle de collecte

L'intervalle de collecte est configurable (`-interval`, 1 minute par défaut) et publié par `hwraid_collection_interval_seconds`.

- `HwraidCollectionStale` s'y adapte d'elle-même : le seuil vaut trois intervalles.
- Les alertes restent actives **10 minutes** sans leur condition (`keep_firing_for`), ce qui couvre au moins deux cycles tant que l'intervalle ne dépasse pas 5 minutes, à condition que le délai de collecte (`-collect-timeout`) et les intervalles de Prometheus restent courts devant eux. Au-delà, un seul cycle en échec retire les séries plus longtemps, et une alerte de panne pourrait s'éteindre. `keep_firing_for` n'accepte pas de durée calculée : il faut alors le porter à au moins deux fois l'intervalle. `HwraidCollectionIntervalTooLong` signale ce cas.

## Cas particulier : volume `unknown`

Sur une grappe à plusieurs niveaux (par exemple quatre miroirs assemblés), le volume de premier niveau passe en `unknown` dès qu'un miroir n'est pas optimal, parce que la propagation de l'état n'est pas encore établie pour ce type d'assemblage. L'alerte utile est alors celle du **miroir** (`HwraidVolumeDegraded` ou `HwraidVolumeRebuilding`) ; `HwraidVolumeStateUnknown` sur le volume parent l'accompagne. Pour regrouper les deux dans une même notification, regrouper par `controller` dans Alertmanager.

## Cache d'écriture

Les règles du cache d'écriture évaluent chaque volume, membres d'un assemblage compris. Sur l'hôte de référence, les volumes membres ont toujours eu le même réglage et le même état que leur volume de premier niveau, mais rien ne le garantit : une même cause donne donc une alerte par volume, à regrouper par `controller` dans Alertmanager.

- `HwraidVolumeWriteCacheUnprotected` est l'alerte de risque immédiat. Elle s'appuie sur l'état effectif du cache, pas sur le réglage : en mode « seulement si protégé », une carte dont le module tombe en panne coupe d'elle-même le cache, et l'alerte ne se déclenche pas. Elle ne retient que les protections dont le défaut est établi (absente, `failed`, `charging`) ; une protection `unknown` donne `HwraidVolumeWriteCacheProtectionUnknown`, de gravité moindre.
- Après un réglage en mode « seulement si protégé » alors que la protection est indisponible, ou en écriture directe, `hwraid_volume_write_cache_enabled` doit passer à 0 pour chaque volume concerné, sur une collecte commencée après le changement (date `hwraid_collection_last_success_timestamp_seconds{collector="volumes"}` postérieure à l'heure du changement d'au moins le délai d'un cycle) : c'est la confirmation du mode d'écriture effectif, pas une preuve que les écritures déjà acceptées ont été vidées. Avec une protection `optimal`, le mode « seulement si protégé » laisse au contraire le cache actif (1).
- `HwraidVolumeWriteCacheUnknown` signale une perte de visibilité : la lecture réussit, mais le réglage ou l'état du cache n'est pas reconnu. Un état inconnu empêche `HwraidVolumeWriteCacheUnprotected`, `HwraidVolumeWriteCacheProtectionUnknown` et `HwraidVolumeWriteCacheInactive` de se déclencher pour ce volume ; un réglage inconnu empêche `HwraidVolumeWriteCacheAlwaysOn` et `HwraidVolumeWriteCacheInactive`.
- `HwraidVolumeWriteCacheAlwaysOn` signale le réglage qui transforme une panne du module en risque de perte.
- `HwraidVolumeWriteCacheInactive` sert notamment après le remplacement d'un module : la protection redevient `optimal`, et le cache doit se réactiver.

## Portée et limites connues

- `HwraidVolumeWriteCacheUnknown` porte sur les volumes qui publient un réglage de cache (`hwraid_volume_write_cache_policy`), ce que fait aacraid pour chaque volume, même avec `policy="unknown"`. Un module futur qui ne publierait ni réglage ni état pour un volume ne déclencherait pas cette alerte.
- `HwraidCollectionIntermittent` voit environ trois échecs en une heure. Des échecs plus espacés (un toutes les 40 minutes, par exemple) peuvent encore remettre à zéro le délai d'une alerte longue (`HwraidCacheProtectionNotReady`, 2 h) sans être signalés.
- `HwraidExporterDown` ne voit qu'une instance qui a déjà publié des métriques `hwraid_` dans les dernières 24 h. Pour signaler une cible qui n'a jamais répondu, ajouter une règle sur le nom de job de votre configuration, par exemple `up{job="hwraid"} == 0`.

## Tester les règles

Avec l'image officielle de Prometheus :

```bash
cd deploy/prometheus && docker run --rm --network none -v "$PWD":/r:ro -w /r --entrypoint promtool prom/prometheus:v3.7.3 test rules hwraid-exporter.rules.test.yml
```

Les tests couvrent : la survie d'une alerte de panne à un trou de lecture, l'absence d'alerte sur un état « dégradé » ou « en panne » passager, le passage de « dégradé » à « en reconstruction », les pannes de disque et de volume, la charge normale du module de protection sans alerte, la péremption adaptée à l'intervalle (60 s et 600 s), deux hôtes au même identifiant de contrôleur, la lecture jamais réussie, l'intervalle trop long, la disparition d'un volume (encore signalée 30 minutes après, pas pendant une lecture en échec), la disparition d'un contrôleur (pas pendant une découverte en échec ni sur une mise à jour du firmware), la découverte en échec, la collecte jamais publiée, l'exporter injoignable, l'absence de tout exporter, la fin de l'alerte de protection du cache quand le module est retiré, et pour chaque autre alerte son déclenchement après le délai puis son retour à la normale. Chaque cas a été vérifié en réintroduisant le défaut qu'il couvre.
