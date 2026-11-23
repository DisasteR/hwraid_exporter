# Journal des versions

## v0.1.0 — 2026-11-23

Première version. Module `aacraid`, qualifié sur Adaptec 6805, firmware 5.3-0[19204].

- État des volumes (miroirs, assemblages) et des disques membres, niveau RAID normalisé.
- Inventaire des disques (fabricant, modèle, firmware, numéro de série, WWN, capacité), emplacement (connecteur, position, phy) et lien avec le périphérique Linux (`sgX`).
- État du module de protection du cache (ZMM).
- Suivi de la collecte : succès par collecteur, date du dernier succès, intervalle, délai par carte.
- Règles d'alerte d'exemple testées avec `promtool`.
- Image Docker (`ghcr.io/disaster/hwraid_exporter`) et unité systemd.
