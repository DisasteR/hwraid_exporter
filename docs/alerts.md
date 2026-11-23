# Règles d'alerte d'exemple

Fichier : [`deploy/prometheus/hwraid-exporter.rules.yml`](../deploy/prometheus/hwraid-exporter.rules.yml), testé par [`hwraid-exporter.rules.test.yml`](../deploy/prometheus/hwraid-exporter.rules.test.yml). Les métriques utilisées sont décrites dans [metrics.md](metrics.md).

Ces règles sont un point de départ : seuils, durées et gravités sont à adapter à votre exploitation.

## Principes

- **Une panne ne s'éteint pas sur un trou de lecture.** Quand une lecture échoue, l'exporter retire les séries d'état au lieu de publier une valeur non observée, et Prometheus les marque périmées. Les règles d'état lisent donc le maximum sur 10 minutes (`max_over_time(…[10m])`) : une alerte de panne survit à un échec de lecture ponctuel.
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
| `HwraidVolumeDisappeared` | volume présent il y a 15 min, absent alors que la lecture réussit | immédiat | warning | suppression ou perte du volume |
| `HwraidCacheProtectionFailed` | protection du cache `failed` | 5 min | warning | remplacer le module ; le cache d'écriture n'est plus protégé contre une coupure |
| `HwraidCacheProtectionNotReady` | protection du cache `charging` | 2 h | warning | une charge normale dure quelques minutes |
| `HwraidCollectionStale` | aucun succès depuis plus de 3 intervalles de collecte | 1 min | warning | consulter le journal de l'exporter |
| `HwraidCollectionNeverSucceeded` | lecture en échec, jamais réussie depuis le démarrage | 15 min | warning | consulter le journal de l'exporter |
| `HwraidCollectionIntervalTooLong` | intervalle de collecte supérieur à 5 min | immédiat | info | allonger les fenêtres des règles d'état |
| `HwraidManagementNodeInvalid` | nœud de gestion invalide | 5 min | warning | erreur de déploiement |
| `HwraidControllerUnsupported` | carte sans module | 1 h | info | carte non surveillée |
| `HwraidExporterAbsent` | aucune métrique `hwraid_` | 10 min | warning | exporter arrêté ou non collecté |

## Adapter les règles à l'intervalle de collecte

L'intervalle de collecte est configurable (`-interval`, 1 minute par défaut) et publié par `hwraid_collection_interval_seconds`.

- `HwraidCollectionStale` s'y adapte d'elle-même : le seuil vaut trois intervalles.
- Les règles d'état lisent le maximum sur **10 minutes**, ce qui couvre au moins deux cycles tant que l'intervalle ne dépasse pas 5 minutes. Au-delà, un seul cycle en échec retire les séries d'état plus longtemps que la fenêtre, et une alerte de panne pourrait s'éteindre. PromQL n'accepte pas de durée calculée dans une fenêtre : il faut alors porter ces fenêtres à au moins deux fois l'intervalle. `HwraidCollectionIntervalTooLong` signale ce cas.

## Cas particulier : volume `unknown`

Sur une grappe à plusieurs niveaux (par exemple quatre miroirs assemblés), le volume de premier niveau passe en `unknown` dès qu'un miroir n'est pas optimal, parce que la propagation de l'état n'est pas encore établie pour ce type d'assemblage. L'alerte utile est alors celle du **miroir** (`HwraidVolumeDegraded` ou `HwraidVolumeRebuilding`) ; `HwraidVolumeStateUnknown` sur le volume parent l'accompagne. Pour regrouper les deux dans une même notification, regrouper par `controller` dans Alertmanager.

## Règle prévue

**Cache d'écriture actif sans protection.** L'alerte la plus utile pour une carte dont le module de protection est en panne est « cache d'écriture en mode différé (write-back) alors que la protection n'est pas optimale ». Elle attend la métrique `hwraid_volume_write_cache_enabled` (voir les métriques prévues dans [metrics.md](metrics.md)). En attendant, `HwraidCacheProtectionFailed` couvre le risque de façon plus large.

## Tester les règles

Avec l'image officielle de Prometheus :

```bash
cd deploy/prometheus && docker run --rm --network none -v "$PWD":/r:ro -w /r --entrypoint promtool prom/prometheus:v3.7.3 test rules hwraid-exporter.rules.test.yml
```

Les tests couvrent : la survie d'une alerte de panne à un trou de lecture, le passage de « dégradé » à « en reconstruction », les pannes de disque et de volume, la charge normale du module de protection sans alerte, la péremption adaptée à l'intervalle (60 s et 600 s), la lecture jamais réussie, l'intervalle trop long, la disparition d'un volume et l'absence de l'exporter.
