// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package aacraid

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// Requêtes émises par l'outil de référence (version 2.05) sur l'Adaptec 6805
// (firmware 5.3-0[19204]) : en-tête de 12 octets, puis données. Les octets
// 12 à 31 de la capture sont des adresses propres à l'outil ; les autres
// octets de données sont nuls.
const (
	capturedAdapterInfoHeader = "01000000" + "bf02" + "01" + "00" + "0400" + "0002"
	capturedEnvHeader         = "01001000" + "2303" + "01" + "00" + "0400" + "0002"
	capturedEnvData           = "01000000"
)

// Réponses enregistrées au même moment. 703 : tronquée à la taille annoncée
// (Size = 120, la suite du tampon est un reste de la requête de l'outil),
// numéro de série de la carte et octets 12-31 de l'en-tête mis à zéro.
// 803 : tronquée après le dernier champ lu (les données suivantes ne sont
// pas interprétées), octets 12-31 de l'en-tête mis à zéro. ARCCONF affichait
// 49 °C, état « Normal ».
const (
	capturedAdapterInfo = "00000000bf020100780000020000000000000000000000000000000000000000" +
		"2b000000090000000c0000009b0200000014000200ecff1d0000002000040305044b000000040305044b0000000211800000000000040305044b000000000000000000000000000000000000020000007c1d1f1401000000"
	capturedEnv = "000000002303010000020002000000000000000000000000000000000000000000000000" +
		"416e616c6f67204465766963652041444d313033320000000000000000000000310000005f0064008000"
)

func padFIB(t *testing.T, s string) []byte {
	t.Helper()
	b := make([]byte, FIBSize)
	copy(b, mustHex(t, s))
	return b
}

// Exigence S4 : partie significative identique à la capture, reste à zéro ;
// le pilote copie exactement le tampon (max(Size + 32, SenderSize) = 512).
func TestEnvironmentRequestsMatchCapture(t *testing.T) {
	for _, tc := range []struct {
		name       string
		req        []byte
		head, data string
	}{
		{"703", AdapterInfoRequest(), capturedAdapterInfoHeader, ""},
		{"803", EnvironmentRequest(), capturedEnvHeader, capturedEnvData},
	} {
		if len(tc.req) != FIBSize {
			t.Fatalf("%s : %d octets", tc.name, len(tc.req))
		}
		if want := mustHex(t, tc.head); !bytes.Equal(tc.req[:len(want)], want) {
			t.Fatalf("%s : en-tête %x, capture %x", tc.name, tc.req[:len(want)], want)
		}
		data := mustHex(t, tc.data)
		if !bytes.Equal(tc.req[FIBHeaderSize:FIBHeaderSize+len(data)], data) {
			t.Fatalf("%s : données %x, capture %x", tc.name, tc.req[FIBHeaderSize:FIBHeaderSize+len(data)], data)
		}
		for i := 12; i < FIBSize; i++ {
			if (i < FIBHeaderSize || i >= FIBHeaderSize+len(data)) && tc.req[i] != 0 {
				t.Fatalf("%s : octet %d = %#x, attendu 0", tc.name, i, tc.req[i])
			}
		}
		size := int(binary.LittleEndian.Uint16(tc.req[8:])) + FIBHeaderSize
		if sender := int(binary.LittleEndian.Uint16(tc.req[10:])); sender > size {
			size = sender
		}
		if size != FIBSize {
			t.Fatalf("%s : plage copiée par le pilote %d octets", tc.name, size)
		}
		if err := CheckFIBRequest(tc.req); err != nil {
			t.Fatalf("%s : requête refusée par la liste blanche : %v", tc.name, err)
		}
	}
}

// La liste blanche refuse toute variante : autre sélecteur, XferState de la
// 703 sur la 803, données parasites.
func TestEnvironmentRequestVariantsRefused(t *testing.T) {
	for name, mut := range map[string]func([]byte){
		"sélecteur 2":         func(b []byte) { b[FIBHeaderSize] = 2 },
		"sans 0x100000":       func(b []byte) { binary.LittleEndian.PutUint32(b, 1) },
		"donnée parasite":     func(b []byte) { b[FIBHeaderSize+4] = 1 },
		"taille de données 8": func(b []byte) { binary.LittleEndian.PutUint16(b[8:], 8) },
	} {
		b := EnvironmentRequest()
		mut(b)
		if err := CheckFIBRequest(b); !errors.Is(err, ErrFIBNotAllowed) {
			t.Errorf("%s : variante acceptée (%v)", name, err)
		}
	}
	b := AdapterInfoRequest()
	b[FIBHeaderSize] = 1
	if err := CheckFIBRequest(b); !errors.Is(err, ErrFIBNotAllowed) {
		t.Errorf("703 avec donnée : variante acceptée (%v)", err)
	}
}

func TestDecodeAdapterInfo(t *testing.T) {
	a, err := DecodeAdapterInfo(padFIB(t, capturedAdapterInfo))
	if err != nil {
		t.Fatal(err)
	}
	if a.SupportedOptions != 0x141f1d7c || !a.HeatSensor() {
		t.Fatalf("%#x, capteur %v", a.SupportedOptions, a.HeatSensor())
	}
	b := padFIB(t, capturedAdapterInfo)
	binary.LittleEndian.PutUint32(b[FIBHeaderSize+adapterSupportedOptions:], 0x141f1d7c&^SupportedHeatSensor)
	if a, err := DecodeAdapterInfo(b); err != nil || a.HeatSensor() {
		t.Fatalf("bit absent : capteur %v (%v)", a.HeatSensor(), err)
	}
	// Size trop court pour couvrir SupportedOptions : la valeur pourrait
	// venir de la requête.
	b = padFIB(t, capturedAdapterInfo)
	binary.LittleEndian.PutUint16(b[fibSize:], FIBHeaderSize+adapterSupportedOptions)
	if _, err := DecodeAdapterInfo(b); err == nil {
		t.Fatal("réponse tronquée acceptée")
	}
	b = padFIB(t, capturedAdapterInfo)
	binary.LittleEndian.PutUint16(b[fibCommand:], CmdRequestSupplementAdapterInfo)
	if _, err := DecodeAdapterInfo(b); err == nil {
		t.Fatal("réponse d'une autre commande acceptée")
	}
}

func TestDecodeEnvironment(t *testing.T) {
	e, err := DecodeEnvironment(padFIB(t, capturedEnv))
	if err != nil {
		t.Fatal(err)
	}
	if e != (Environment{TemperatureCelsius: 49, ThresholdLoCelsius: 95, ThresholdHiCelsius: 100}) || e.Abnormal() {
		t.Fatalf("%+v, anormal %v", e, e.Abnormal())
	}
	// Champs sur 16 bits : un octet haut non nul compte.
	b := padFIB(t, capturedEnv)
	binary.LittleEndian.PutUint16(b[FIBHeaderSize+envTemperature:], 0x0131)
	if e, _ := DecodeEnvironment(b); e.TemperatureCelsius != 0x131 || !e.Abnormal() {
		t.Fatalf("champ 16 bits mal lu : %+v", e)
	}
	// Règle de l'outil de référence : anormal à partir du seuil haut inclus.
	binary.LittleEndian.PutUint16(b[FIBHeaderSize+envTemperature:], 100)
	if e, _ := DecodeEnvironment(b); !e.Abnormal() {
		t.Fatal("seuil haut atteint : état normal")
	}
	binary.LittleEndian.PutUint16(b[FIBHeaderSize+envTemperature:], 99)
	if e, _ := DecodeEnvironment(b); e.Abnormal() {
		t.Fatal("sous le seuil haut : état anormal")
	}
	b = padFIB(t, capturedEnv)
	binary.LittleEndian.PutUint32(b[FIBHeaderSize:], 1)
	if _, err := DecodeEnvironment(b); err == nil {
		t.Fatal("statut d'échec accepté")
	}
	b = padFIB(t, capturedEnv)
	binary.LittleEndian.PutUint16(b[fibSize:], FIBHeaderSize+envThresholdHi)
	if _, err := DecodeEnvironment(b); err == nil {
		t.Fatal("réponse tronquée acceptée")
	}
}
