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
	{
		Name:    "FSACTL_SENDFIB",
		Ioctl:   IoctlSendFIB,
		Size:    FIBSize,
		Payload: CheckFIBRequest,
		Proof:   "RequestSupplementAdapterInfo (706) et lectures VM_Ioctl de présence et d'état de la protection du cache : partie significative identique aux captures de l'outil de référence (docs/aacraid-interface.md, sections 6 et 8)",
	},
}

// NewAllowlist construit la liste blanche du module.
func NewAllowlist() (*allowlist.List, error) { return allowlist.New(Entries...) }
