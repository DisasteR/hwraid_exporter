# Règles d'alerte d'exemple

Fichier : [`deploy/prometheus/hwraid-exporter.rules.yml`](../deploy/prometheus/hwraid-exporter.rules.yml), testé par [`hwraid-exporter.rules.test.yml`](../deploy/prometheus/hwraid-exporter.rules.test.yml). Les métriques utilisées sont décrites dans [metrics.md](metrics.md).

Ces règles sont un point de départ : seuils, durées et gravités sont à adapter à votre exploitation.

## Principes

- **L'état publié prime ; la dernière valeur connue ne couvre qu'un trou de lecture.** Quand une lecture échoue, l'exporter retire les séries d'état au lieu de publier une valeur non observée, et Prometheus les marque périmées. Chaque règle d'état prend donc l'état courant, ou, si l'objet n'a plus aucune série d'état, sa dernière valeur sur 10 minutes (`last_over_time(…[10m]) unless on (…) <métrique>`). Une panne survit ainsi à un échec de lecture ponctuel, et un état passager ne prolonge pas l'alerte une fois l'objet revenu à un autre état : les délais (`for`) portent sur l'état réel. Cas limite assumé : un objet redevenu sain puis aussitôt illisible (moins de 10 minutes) peut faire revenir l'alerte pendant le trou ; `HwraidCollectionStale` l'accompagne.
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
| `HwraidVolumeWriteCacheUnprotected` | cache d'écriture actif (`hwraid_volume_write_cache_enabled` 1) alors que la protection est absente, `failed` ou `charging` | 5 min | critical | une coupure de courant, ou un redémarrage sans vidage du cache, perd les écritures en attente : régler le cache en mode « seulement si protégé » (`wbb`) ou le désactiver |
| `HwraidVolumeWriteCacheProtectionUnknown` | cache d'écriture actif alors que la protection est `unknown` | 30 min | warning | protection non garantie : vérifier le module avec l'outil du constructeur |
| `HwraidVolumeWriteCacheAlwaysOn` | réglage `write_back_always` | 15 min | warning | risque latent : une panne du module laisserait le cache actif ; préférer `write_back_when_protected` |
| `HwraidVolumeWriteCacheInactive` | cache d'écriture inactif alors que le réglage le demande et que la protection est `optimal` | 2 h | info | écritures ralenties : la carte n'a pas réactivé le cache, vérifier avec l'outil du constructeur |
| `HwraidVolumeWriteCacheUnknown` | lecture des volumes réussie, mais réglage `unknown` ou état du cache non publié | 30 min | warning | les autres alertes de cache ne couvrent plus ce volume : vérifier avec l'outil du constructeur |
| `HwraidCollectionStale` | aucun succès depuis plus de 3 intervalles de collecte (lecture en échec ou hors délai) | 1 min | warning | consulter le journal de l'exporter ; `cannot allocate memory` sur toutes les lectures : réserve du pilote très probablement épuisée, le prochain redémarrage ne videra pas le cache de la carte ([aacraid-defaut-pilote.md](aacraid-defaut-pilote.md)) |
| `HwraidCollectionNeverSucceeded` | lecture en échec, jamais réussie depuis le démarrage | 15 min | warning | consulter le journal de l'exporter ; même cas de `cannot allocate memory` |
| `HwraidCollectionIntervalTooLong` | intervalle de collecte supérieur à 5 min | immédiat | info | allonger les fenêtres des règles d'état |
| `HwraidManagementNodeInvalid` | nœud de gestion non préparé, non vérifié ou désignant une autre carte | 5 min | warning | erreur de déploiement, carte changée |
| `HwraidControllerUnsupported` | carte sans module | 1 h | info | carte non surveillée |
| `HwraidControllerDisappeared` | contrôleur vu dans les dernières 24 h, absent alors que la découverte réussit | immédiat, pendant 24 h | warning | carte retirée, pilote déchargé ou identité changée |
| `HwraidDiscoveryFailed` | découverte sysfs en échec (au moins un hôte SCSI illisible) | 15 min | warning | une carte peut ne plus être surveillée |
| `HwraidSnapshotNotReady` | exporter joignable mais aucune collecte publiée | 10 min | warning | consulter le journal de l'exporter |
| `HwraidExporterDown` | instance ayant publié des métriques `hwraid_` dans les dernières 24 h, `up == 0` | 10 min | warning | exporter arrêté ou injoignable ; une cible retirée de la configuration de Prometheus n'a plus de série `up` et n'est pas vue |
| `HwraidExporterAbsent` | aucune métrique `hwraid_` sur l'ensemble des instances | 10 min | warning | filet global ; ne voit pas la perte d'une instance parmi plusieurs |

## Adapter les règles à l'intervalle de collecte

L'intervalle de collecte est configurable (`-interval`, 1 minute par défaut) et publié par `hwraid_collection_interval_seconds`.

- `HwraidCollectionStale` s'y adapte d'elle-même : le seuil vaut trois intervalles.
- Les règles d'état retiennent la dernière valeur connue sur **10 minutes**, ce qui couvre au moins deux cycles tant que l'intervalle ne dépasse pas 5 minutes. Au-delà, un seul cycle en échec retire les séries d'état plus longtemps que la fenêtre, et une alerte de panne pourrait s'éteindre. PromQL n'accepte pas de durée calculée dans une fenêtre : il faut alors porter ces fenêtres à au moins deux fois l'intervalle. `HwraidCollectionIntervalTooLong` signale ce cas.

## Cas particulier : volume `unknown`

Sur une grappe à plusieurs niveaux (par exemple quatre miroirs assemblés), le volume de premier niveau passe en `unknown` dès qu'un miroir n'est pas optimal, parce que la propagation de l'état n'est pas encore établie pour ce type d'assemblage. L'alerte utile est alors celle du **miroir** (`HwraidVolumeDegraded` ou `HwraidVolumeRebuilding`) ; `HwraidVolumeStateUnknown` sur le volume parent l'accompagne. Pour regrouper les deux dans une même notification, regrouper par `controller` dans Alertmanager.

## Cache d'écriture

Les règles du cache d'écriture ne portent que sur les volumes de premier niveau (`parent` vide) : les volumes assemblés portent le même réglage, et une même cause donnerait sinon une alerte par volume.

- `HwraidVolumeWriteCacheUnprotected` est l'alerte de risque immédiat. Elle s'appuie sur l'état effectif du cache, pas sur le réglage : en mode « seulement si protégé », une carte dont le module tombe en panne coupe d'elle-même le cache, et l'alerte ne se déclenche pas. Elle ne retient que les protections dont le défaut est établi (absente, `failed`, `charging`) ; une protection `unknown` donne `HwraidVolumeWriteCacheProtectionUnknown`, de gravité moindre.
- **Trous de lecture.** Pendant une lecture en échec (`hwraid_collection_success` à 0 ou absent pour `volumes` ou `cache_protection`), chaque série reprend sa dernière valeur connue sur 10 minutes : un échec passager ne remet pas à zéro le délai d'une alerte. Une lecture réussie ne garde que les valeurs publiées : un volume supprimé ou un état changé éteint l'alerte aussitôt.
- `HwraidVolumeWriteCacheUnknown` signale la perte de visibilité : la lecture réussit, mais le réglage ou l'état du cache n'est pas reconnu, et les autres alertes ne peuvent plus se déclencher pour ce volume.
- `HwraidVolumeWriteCacheAlwaysOn` signale le réglage qui transforme une panne du module en risque de perte.
- `HwraidVolumeWriteCacheInactive` sert notamment après le remplacement d'un module : la protection redevient `optimal`, et le cache doit se réactiver.

## Tester les règles

Avec l'image officielle de Prometheus :

```bash
cd deploy/prometheus && docker run --rm --network none -v "$PWD":/r:ro -w /r --entrypoint promtool prom/prometheus:v3.7.3 test rules hwraid-exporter.rules.test.yml
```

Les tests couvrent : la survie d'une alerte de panne à un trou de lecture, l'absence d'alerte sur un état « dégradé » ou « en panne » passager, le passage de « dégradé » à « en reconstruction », les pannes de disque et de volume, la charge normale du module de protection sans alerte, la péremption adaptée à l'intervalle (60 s et 600 s), deux hôtes au même identifiant de contrôleur, la lecture jamais réussie, l'intervalle trop long, la disparition d'un volume (encore signalée 30 minutes après, pas pendant une lecture en échec), la disparition d'un contrôleur (pas pendant une découverte en échec ni sur une mise à jour du firmware), la découverte en échec, la collecte jamais publiée, l'exporter injoignable, l'absence de tout exporter, la fin de l'alerte de protection du cache quand le module est retiré, et pour chaque autre alerte son déclenchement après le délai puis son retour à la normale. Chaque cas a été vérifié en réintroduisant le défaut qu'il couvre.
