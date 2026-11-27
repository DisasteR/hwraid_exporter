// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"encoding/binary"
	"fmt"
)

// Commandes FIB publiques (FreeBSD aacraid_reg.h ; RequestAdapterInfo est
// aussi envoyée par le pilote Linux à l'initialisation, aac_get_adapter_info).
const (
	CmdRequestAdapterInfo     = 703 // struct aac_adapter_info
	CmdAdapterEnvironmentInfo = 803 // capteurs de température
)

// SupportedHeatSensor est le bit AAC_SUPPORTED_HEAT_SENSOR de SupportedOptions
// (FreeBSD aacraid_reg.h) : la carte a un capteur de température lisible par
// AdapterEnvironmentInfo. L'outil de référence n'envoie cette commande que si
// le bit est annoncé (docs/aacraid-interface.md, section 5).
const SupportedHeatSensor = 0x80000

// Champs publics de struct aac_adapter_info (FreeBSD aacraid_reg.h),
// relatifs au début des données : sept mots, quatre révisions de 8 octets,
// deux mots, le numéro de série sur 8 octets et le mot batterie précèdent
// SupportedOptions. Seul ce champ est lu.
const (
	adapterSupportedOptions = 80
	adapterMinData          = adapterSupportedOptions + 4
)

// AdapterInfoRequest construit la requête RequestAdapterInfo : même forme
// que RequestSupplementAdapterInfo, un mot de données nul (requête identique
// à celle de l'outil de référence, octets d'adresse mis à part).
func AdapterInfoRequest() []byte {
	b := make([]byte, FIBSize)
	le := binary.LittleEndian
	le.PutUint32(b[fibXferState:], reqXferState)
	le.PutUint16(b[fibCommand:], CmdRequestAdapterInfo)
	b[fibStructType] = reqStructType
	le.PutUint16(b[fibSize:], reqDataSize)
	le.PutUint16(b[fibSenderSize:], FIBSize)
	return b
}

// AdapterInfo regroupe les champs utilisés de la réponse à RequestAdapterInfo.
type AdapterInfo struct {
	SupportedOptions uint32
}

// HeatSensor indique si la carte annonce un capteur de température.
func (a AdapterInfo) HeatSensor() bool { return a.SupportedOptions&SupportedHeatSensor != 0 }

// SupportedSupplementInfo est le bit 16 de SupportedOptions
// (AAC_OPT_SUPPLEMENT_ADAPTER_INFO, Linux aacraid.h) : la carte répond à
// RequestSupplementAdapterInfo (706). Le pilote Linux et l'outil de
// référence n'envoient la 706 que si ce bit est annoncé.
const SupportedSupplementInfo = 0x10000

// SupplementInfo indique si la carte annonce la 706.
func (a AdapterInfo) SupplementInfo() bool { return a.SupportedOptions&SupportedSupplementInfo != 0 }

// SupportedNativeHBA est le bit 25 de SupportedOptions : option HBA natif
// annoncée (docs/aacraid-interface.md, section 6 bis ; le mode courant de la
// carte n'est pas testé).
const SupportedNativeHBA = 0x2000000

// NativeHBA indique l'option HBA natif.
func (a AdapterInfo) NativeHBA() bool { return a.SupportedOptions&SupportedNativeHBA != 0 }

// SupportedLargeTransfers est le bit 15 de SupportedOptions : transferts
// paginés en blocs de 2 048 octets (CDC, section 6.11, forme 3), non encore
// implémentés. Absent sur la carte de référence (28 réponses 703 capturées).
const SupportedLargeTransfers = 0x8000

// LargeTransfers indique le bit 15 de SupportedOptions.
func (a AdapterInfo) LargeTransfers() bool { return a.SupportedOptions&SupportedLargeTransfers != 0 }

// SupportedAlarm est le bit 11 de SupportedOptions (AAC_OPT_ALARM, Linux
// aacraid.h) : la carte a une alarme sonore. L'outil de référence n'envoie
// CT_ALARM_GET_STATE que si ce bit est annoncé (présent sur la carte de
// référence).
const SupportedAlarm = 0x800

// Alarm indique le bit 11 de SupportedOptions.
func (a AdapterInfo) Alarm() bool { return a.SupportedOptions&SupportedAlarm != 0 }

// DecodeAdapterInfo décode la réponse à RequestAdapterInfo. Size inclut
// l'en-tête : il doit couvrir SupportedOptions et tenir dans le tampon.
func DecodeAdapterInfo(b []byte) (AdapterInfo, error) {
	d, err := fibResponse(b, CmdRequestAdapterInfo, adapterMinData)
	if err != nil {
		return AdapterInfo{}, err
	}
	return AdapterInfo{SupportedOptions: binary.LittleEndian.Uint32(d[adapterSupportedOptions:])}, nil
}

// La requête AdapterEnvironmentInfo de l'outil de référence porte, en plus
// de HostOwned, le bit 0x100000 de XferState (ApiFib, Linux aacraid.h),
// comme ses lectures de la protection du cache et contrairement à ses
// requêtes 703 et 706 ; son unique mot de données vaut 1. Les deux sont
// repris tels quels : la requête est identique à la capture
// (docs/aacraid-interface.md, section 8).
const (
	envXferState = 0x100001
	envSelector  = 1
)

// EnvironmentRequest construit la requête AdapterEnvironmentInfo.
func EnvironmentRequest() []byte {
	b := make([]byte, FIBSize)
	le := binary.LittleEndian
	le.PutUint32(b[fibXferState:], envXferState)
	le.PutUint16(b[fibCommand:], CmdAdapterEnvironmentInfo)
	b[fibStructType] = reqStructType
	le.PutUint16(b[fibSize:], reqDataSize)
	le.PutUint16(b[fibSenderSize:], FIBSize)
	le.PutUint32(b[FIBHeaderSize:], envSelector)
	return b
}

// Champs de la réponse AdapterEnvironmentInfo, relatifs au début des
// données, lus par l'outil de référence (versions 2.05 et 3.07) en mots de
// 16 bits [interopérabilité] ; nommés d'après sa sortie JSON.
const (
	envStatus      = 0  // 0 : succès
	envTemperature = 36 // heatSensorTemperature, en °C
	envThresholdLo = 40 // heatSensorThresholdLo
	envThresholdHi = 42 // heatSensorThresholdHi : état anormal à partir de ce seuil
	envMinData     = envThresholdHi + 2
)

// Environment est la lecture du capteur de température de la carte.
type Environment struct {
	TemperatureCelsius uint16
	ThresholdLoCelsius uint16
	ThresholdHiCelsius uint16
}

// Abnormal reprend la règle de l'outil de référence : température égale ou
// supérieure au seuil haut de la carte.
func (e Environment) Abnormal() bool { return e.TemperatureCelsius >= e.ThresholdHiCelsius }

// DecodeEnvironment décode la réponse à AdapterEnvironmentInfo ; un statut
// non nul est une erreur.
func DecodeEnvironment(b []byte) (Environment, error) {
	d, err := fibResponse(b, CmdAdapterEnvironmentInfo, envMinData)
	if err != nil {
		return Environment{}, err
	}
	le := binary.LittleEndian
	if st := le.Uint32(d[envStatus:]); st != 0 {
		return Environment{}, fmt.Errorf("%d response: status %#x", CmdAdapterEnvironmentInfo, st)
	}
	return Environment{
		TemperatureCelsius: le.Uint16(d[envTemperature:]),
		ThresholdLoCelsius: le.Uint16(d[envThresholdLo:]),
		ThresholdHiCelsius: le.Uint16(d[envThresholdHi:]),
	}, nil
}

// fibResponse vérifie l'en-tête d'une réponse FIB simple (taille du tampon,
// commande, Size couvrant minData octets de données sans dépasser le
// tampon) et rend ses données.
func fibResponse(b []byte, cmd uint16, minData int) ([]byte, error) {
	if len(b) != FIBSize {
		return nil, fmt.Errorf("%d response: %d bytes received, %d expected", cmd, len(b), FIBSize)
	}
	le := binary.LittleEndian
	if got := le.Uint16(b[fibCommand:]); got != cmd {
		return nil, fmt.Errorf("%d response: command %d in header", cmd, got)
	}
	size := int(le.Uint16(b[fibSize:]))
	if size < FIBHeaderSize+minData || size > FIBSize {
		return nil, fmt.Errorf("%d response: size %d outside [%d, %d]", cmd, size, FIBHeaderSize+minData, FIBSize)
	}
	return b[FIBHeaderSize:], nil
}
