// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"fmt"
)

// CT_ALARM_GET_STATE (sous-commande 190 de VM_ContainerConfig, nom de
// l'outil de référence) : état de l'alarme sonore de la carte.
//
// Forme de la requête : en-tête des requêtes paginées, données [2, 190, 1,
// 0, 0, 0xffffffff, 0, 8], reste à zéro ; identique, octet pour octet après
// mise à zéro des adresses de l'en-tête, aux 14 requêtes capturées de
// l'outil de référence sur la carte de référence. Bit 13 de FeatureBits :
// mot 7 | SizeHighBit, comme pour le comptage des conteneurs (l'outil
// l'applique dans sa routine commune d'envoi). L'outil ne l'envoie que si
// la carte annonce AAC_OPT_ALARM (bit 11 de SupportedOptions, 703).
const ctAlarmGetState = 190

// Paramètre et taille (mots 2 et 7) de la requête de l'outil.
const (
	alarmParam = 1
	alarmSize  = 8
)

// Réponse : mot 2 recopié par l'outil (CT_SendReceiveFIB, 2.05 0x8ddbdd,
// 3.07 0x8b0079) et exigé égal à 1 par CT_AlarmCmdOut (2.05 0x8df564, 3.07
// 0x8c36a0 : toute autre valeur lève une exception) ; puis _ALARM_INFO, 8 octets à l'offset 0x20 des données ; l'outil
// n'en lit, pour l'état affiché, que le bit 0x02 du premier octet
// (présent : « Enabled », absent : « Disabled »).
const (
	alarmInfo       = 0x20
	alarmEnabledBit = 0x02
)

// AlarmState est l'état de l'alarme sonore tel que l'outil de référence
// l'affiche.
type AlarmState string

// États de l'alarme sonore.
const (
	AlarmEnabled  AlarmState = "enabled"
	AlarmDisabled AlarmState = "disabled"
)

// AlarmRequest construit la requête CT_ALARM_GET_STATE.
func AlarmRequest() []byte {
	return pagedRequest(vmContainerConfig, ctAlarmGetState, alarmParam, 0, 0, 0xffffffff, 0, alarmSize)
}

// DecodeAlarm vérifie la réponse à CT_ALARM_GET_STATE et rend l'état de
// l'alarme. Exigés : commande 500 et Size 496 (forme des réponses paginées,
// comme DecodeContainerCount, contrôles propres à l'exporter), statut nul,
// écho 190 et mot 2 égal à 1 (l'outil lève une exception sinon). Seul le
// bit 0x02 du premier octet de _ALARM_INFO est interprété.
func DecodeAlarm(rsp []byte) (AlarmState, error) {
	if len(rsp) != FIBSize {
		return "", fmt.Errorf("alarm: %d bytes received, %d expected", len(rsp), FIBSize)
	}
	le := binary.LittleEndian
	if cmd := le.Uint16(rsp[fibCommand:]); cmd != cmdContainerCommand {
		return "", fmt.Errorf("alarm: command %d in header", cmd)
	}
	if sz := le.Uint16(rsp[fibSize:]); sz != ChunkRspSize {
		return "", fmt.Errorf("alarm: size field %d, expected %d", sz, ChunkRspSize)
	}
	d := rsp[FIBHeaderSize:]
	if st := le.Uint32(d[0:]); st != 0 {
		return "", fmt.Errorf("alarm: status %d", st)
	}
	if echo := le.Uint32(d[4:]); echo != ctAlarmGetState {
		return "", fmt.Errorf("alarm: subcommand %d, expected %d", echo, ctAlarmGetState)
	}
	if w2 := le.Uint32(d[8:]); w2 != alarmParam {
		return "", fmt.Errorf("alarm: word 2 is %d, expected %d", w2, alarmParam)
	}
	if d[alarmInfo]&alarmEnabledBit != 0 {
		return AlarmEnabled, nil
	}
	return AlarmDisabled, nil
}
