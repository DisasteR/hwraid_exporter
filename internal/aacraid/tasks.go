// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"fmt"
)

// Tâches de fond (reconstruction, vérification…) : commande publique
// AifRequest (700, FreeBSD aacreg.h), lue comme l'outil de référence
// (docs/aacraid-interface.md, section 5) : AifReqJobsForCtr (101) liste les
// tâches d'un conteneur, AifReqJobReport (103) rend le rapport de
// progression d'une tâche. Les autres sous-commandes (104 à 110) agissent
// sur les tâches : elles ne sont jamais envoyées.
const (
	CmdAifRequest    = 700
	AifReqJobsForCtr = 101
	AifReqJobReport  = 103
)

// Requête de l'outil : XferState 0x100001, Size = 0x48 ; mot 0 = sous-
// commande, mot 2 = conteneur ou identifiant de tâche. L'outil n'initialise
// ni l'octet 7 de l'en-tête ni les autres mots (contenu de sa pile,
// différent d'une capture à l'autre) : ils sont à zéro.
const (
	aifXferState = 0x100001
	aifDataSize  = 0x48
)

func aifRequest(sub, arg uint32) []byte {
	b := make([]byte, FIBSize)
	le := binary.LittleEndian
	le.PutUint32(b[fibXferState:], aifXferState)
	le.PutUint16(b[fibCommand:], CmdAifRequest)
	b[fibStructType] = reqStructType
	le.PutUint16(b[fibSize:], aifDataSize)
	le.PutUint16(b[fibSenderSize:], FIBSize)
	le.PutUint32(b[FIBHeaderSize:], sub)
	le.PutUint32(b[FIBHeaderSize+8:], arg)
	return b
}

// TaskListRequest construit la requête AifReqJobsForCtr pour un conteneur.
func TaskListRequest(container uint32) []byte { return aifRequest(AifReqJobsForCtr, container) }

// TaskReportRequest construit la requête AifReqJobReport pour une tâche.
func TaskReportRequest(job uint32) []byte { return aifRequest(AifReqJobReport, job) }

// checkTaskRequest reconnaît une requête de tâche de la forme ci-dessus :
// sous-commande 101 (conteneur jusqu'à MaxContainersLimit) ou 103
// (identifiant de tâche rendu par la carte), tout le reste à zéro.
func checkTaskRequest(b []byte) bool {
	if len(b) != FIBSize {
		return false
	}
	le := binary.LittleEndian
	sub, arg := le.Uint32(b[FIBHeaderSize:]), le.Uint32(b[FIBHeaderSize+8:])
	switch {
	case sub == AifReqJobsForCtr && arg <= MaxContainersLimit, sub == AifReqJobReport:
	default:
		return false
	}
	ref := aifRequest(sub, arg)
	for i := range b {
		if b[i] != ref[i] {
			return false
		}
	}
	return true
}

// Réponse : 8 octets (deux mots), puis des blocs de 48 octets
// (taskBlockSize). L'outil rejette une réponse dont le mot 1 vaut 4 ou 5 ;
// le sens des deux mots n'est pas établi au-delà (réponse sans tâche de la
// carte de référence : 0 et 1).
const (
	taskHead      = 8
	taskBlockSize = 48
	taskRspBase   = FIBHeaderSize + taskHead // Size d'une réponse sans bloc
)

// Disposition d'un bloc, lue par l'outil de référence (versions 2.05 et
// 3.07) : descripteur public (FreeBSD aac_AifJobDesc : identifiant, type,
// conteneur source), puis statut et deux compteurs de 64 bits (FreeBSD :
// aac_AifJobProgressReport, compteurs de 32 bits).
const (
	tbJob       = 0
	tbType      = 4
	tbContainer = 8
	tbStatus    = 0x10
	tbFinal     = 0x18
	tbCurrent   = 0x20
)

// Task est une tâche de fond lue sur la carte.
type Task struct {
	Job, Type, Container, Status uint32
	Final, Current               uint64
}

// taskBlocks vérifie l'en-tête d'une réponse AifRequest et rend ses blocs :
// Size doit couvrir exactement l'en-tête et un nombre entier de blocs (plus
// strict que l'outil, qui arrondit).
func taskBlocks(b []byte, what string) ([][]byte, error) {
	if len(b) != FIBSize {
		return nil, fmt.Errorf("%s: %d bytes received, %d expected", what, len(b), FIBSize)
	}
	le := binary.LittleEndian
	if cmd := le.Uint16(b[fibCommand:]); cmd != CmdAifRequest {
		return nil, fmt.Errorf("%s: command %d in header", what, cmd)
	}
	size := int(le.Uint16(b[fibSize:]))
	if size < taskRspBase || size > FIBSize || (size-taskRspBase)%taskBlockSize != 0 {
		return nil, fmt.Errorf("%s: size %d is not %d plus whole %d-byte blocks", what, size, taskRspBase, taskBlockSize)
	}
	if w1 := le.Uint32(b[FIBHeaderSize+4:]); w1 == 4 || w1 == 5 {
		return nil, fmt.Errorf("%s: error %d", what, w1)
	}
	var out [][]byte
	for off := taskRspBase; off < size; off += taskBlockSize {
		out = append(out, b[off:off+taskBlockSize])
	}
	return out, nil
}

func decodeTask(blk []byte) Task {
	le := binary.LittleEndian
	return Task{Job: le.Uint32(blk[tbJob:]), Type: le.Uint32(blk[tbType:]), Container: le.Uint32(blk[tbContainer:]),
		Status: le.Uint32(blk[tbStatus:]), Final: le.Uint64(blk[tbFinal:]), Current: le.Uint64(blk[tbCurrent:])}
}

// DecodeTaskList décode la réponse à AifReqJobsForCtr : une tâche par bloc
// (seuls l'identifiant, le type et le conteneur servent, comme dans
// l'outil).
func DecodeTaskList(b []byte) ([]Task, error) {
	blks, err := taskBlocks(b, "task list")
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(blks))
	for _, blk := range blks {
		out = append(out, decodeTask(blk))
	}
	return out, nil
}

// DecodeTaskReport décode la réponse à AifReqJobReport : le premier bloc,
// qui doit porter la tâche demandée.
func DecodeTaskReport(b []byte, job uint32) (Task, error) {
	blks, err := taskBlocks(b, "task report")
	if err != nil {
		return Task{}, err
	}
	if len(blks) == 0 {
		return Task{}, fmt.Errorf("task report: no report for task %#x", job)
	}
	t := decodeTask(blks[0])
	if t.Job != job {
		return Task{}, fmt.Errorf("task report: report for task %#x, %#x requested", t.Job, job)
	}
	return t, nil
}

// Statuts publics d'une tâche (FreeBSD AAC_AifJobStatus).
const (
	TaskSucceeded = 1
	TaskFinished  = 2
	TaskSuspended = 101
	TaskRunning   = 102
)

// Finished indique une tâche que la carte déclare terminée (statut 1 ou
// 2) : sa série disparaît (CDC), elle n'est pas publiée.
func (t Task) Finished() bool { return t.Status == TaskSucceeded || t.Status == TaskFinished }

// Ratio rend l'avancement d'une tâche en cours ou suspendue, entre 0 et 1 :
// currentTick / finalTick (CDC). ok est faux, avec la raison, si
// l'avancement n'est pas établi : finalTick nul, currentTick au-delà de
// finalTick, ou autre statut (terminée, abandonnée, échouée, inconnu).
func (t Task) Ratio() (ratio float64, ok bool, why string) {
	switch t.Status {
	case TaskSuspended, TaskRunning:
	default:
		return 0, false, fmt.Sprintf("status %d", t.Status)
	}
	if t.Final == 0 {
		return 0, false, "finalTick is 0"
	}
	if t.Current > t.Final {
		return 0, false, fmt.Sprintf("currentTick %d beyond finalTick %d", t.Current, t.Final)
	}
	return float64(t.Current) / float64(t.Final), true, ""
}

// Types publics de tâche de conteneur (FreeBSD AAC_AifJobType) et nom
// normalisé (docs/metrics.md), regroupés comme l'outil de référence les
// affiche (103 rejoint les reconstructions) ; tout autre type est « other ».
var taskNames = map[uint32]string{
	101: "zero",            // AifJobCtrZero
	103: "rebuild",         // AifJobCtrCreateMirror
	105: "verify",          // AifJobCtrScrubMirror
	106: "rebuild",         // AifJobCtrRebuildRaid5
	107: "verify",          // AifJobCtrScrubRaid5
	108: "migration",       // AifJobCtrMorph
	110: "rebuild",         // AifJobCtrRebuildMirror
	112: "copyback",        // AifJobCtrCopyback
	113: "compaction",      // AifJobCtrCompactRaid5D
	114: "expansion",       // AifJobCtrExpandRaid5D
	115: "rebuild",         // AifJobCtrRebuildRaid6
	116: "verify",          // AifJobCtrScrubRaid6
	117: "snapshot_backup", // AifJobCtrSSBackup
}

// Name rend le nom normalisé du type de la tâche.
func (t Task) Name() string {
	if n, ok := taskNames[t.Type]; ok {
		return n
	}
	return "other"
}
