package aacraid

import "github.com/DisasteR/hwraid_exporter/internal/allowlist"

// Entries est la liste blanche déclarée du module aacraid.
// Toute modification exige une revue et une référence de qualification (S3).
// FSACTL_SENDFIB et FSACTL_SEND_LARGE_FIB n'y figurent pas encore :
// aucune commande firmware n'est autorisée tant qu'elle n'est pas qualifiée.
var Entries = []allowlist.Entry{
	{
		Name:  "FSACTL_GET_HBA_INFO",
		Ioctl: IoctlGetHBAInfo,
		Size:  HBAInfoSize,
		Proof: "drivers/scsi/aacraid/commctrl.c aac_get_hba_info : réponse construite par le pilote, sans FIB",
	},
}

// NewAllowlist construit la liste blanche du module.
func NewAllowlist() (*allowlist.List, error) { return allowlist.New(Entries...) }
