// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import "github.com/DisasteR/hwraid_exporter/internal/allowlist"

// Entries est la liste blanche déclarée du module aacraid.
// Toute modification exige une revue et une référence de qualification (S3).
// FSACTL_SENDFIB et FSACTL_SEND_LARGE_FIB n'acceptent que des requêtes
// qualifiées, contrôlées octet par octet.
var Entries = []allowlist.Entry{
	{
		Name:  "FSACTL_GET_HBA_INFO",
		Ioctl: IoctlGetHBAInfo,
		Size:  HBAInfoSize,
		Proof: "drivers/scsi/aacraid/commctrl.c aac_get_hba_info : réponse construite par le pilote, sans FIB",
	},
	{
		Name:    "FSACTL_SENDFIB",
		Ioctl:   IoctlSendFIB,
		Size:    FIBSize,
		Payload: CheckFIBRequest,
		Proof:   "RequestSupplementAdapterInfo (706), RequestAdapterInfo (703), AdapterEnvironmentInfo (803), lectures VM_Ioctl de présence et d'état de la protection du cache, transfert paginé et CT_GET_CONTAINER_COUNT : partie significative identique aux captures de l'outil de référence ; liste des conteneurs aux tailles autres que 1 032 octets : règle de construction de l'outil, taille liée au comptage de la carte par le transport (docs/aacraid-interface.md, sections 4, 6 et 8)",
	},
	{
		Name:    "FSACTL_SEND_LARGE_FIB",
		Ioctl:   IoctlSendLargeFIB,
		Size:    LargeFIBSize,
		Payload: CheckLargeFIBRequest,
		Proof:   "RequestConnectorMap (708) seule : partie significative identique à la capture de l'outil de référence (docs/aacraid-interface.md, section 5)",
	},
}

// NewAllowlist construit la liste blanche du module.
func NewAllowlist() (*allowlist.List, error) { return allowlist.New(Entries...) }
