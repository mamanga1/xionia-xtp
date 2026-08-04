# XionChat

**Chat cifrado de extremo a extremo. Sin cuentas. Sin número de teléfono. Sin servidor central que lea nada.**

Cliente Android de [Web5-Mesh](https://github.com/mamanga1/Web5-Mesh) — la red overlay soberana que hace el trabajo pesado.

[![Platform](https://img.shields.io/badge/platform-Android-3DDC84?style=flat&logo=android&logoColor=white)](#)
[![Engine](https://img.shields.io/badge/engine-Web5--Mesh-1e2327?style=flat)](https://github.com/mamanga1/Web5-Mesh)
[![Status](https://img.shields.io/badge/status-release%20v1.0.1-brightgreen)](#estado-del-proyecto)
[![Encryption](https://img.shields.io/badge/E2E-Ed25519%20%2F%20X25519%20%2F%20ChaCha20--Poly1305-red)](#)
[![License](https://img.shields.io/badge/license-AGPLv3%20%2B%20Commons%20Clause-blue)](#)

---

## Qué es esto

XionChat es una app de chat con cara de mensajero común — lista de contactos, burbujas, todo lo esperable — pero el motor de abajo no se parece en nada a WhatsApp o Telegram. No hay cuenta, no hay número de teléfono, no hay empresa en el medio guardando con quién hablás.

Tu identidad es un par de claves Ed25519/X25519 que se genera en el dispositivo. Cada mensaje se cifra de extremo a extremo antes de salir. Un **Faro** — un relay ciego, sin estado persistente — lo reenvía sin poder leer el contenido ni saber quién habla con quién más allá de un identificador de 4 bytes derivado de la clave pública.

Todo eso lo hace [Web5-Mesh](https://github.com/mamanga1/Web5-Mesh), el motor en Go de esta red. XionChat es la puerta de entrada para usarlo desde el celular sin tocar una terminal. En dicho repositorio tenés las guías de cómo levantar tus propios [Faros](https://github.com/mamanga1/Web5-Mesh/blob/main/docs/FAROS.md).

---

## Por qué existe esto

Web5-Mesh se prueba hoy principalmente desde una terminal (`mesh shell`). Eso funciona, pero deja afuera a cualquiera que no quiera lidiar con una consola. XionChat existe para que se pueda tocar el alma del protocolo — identidad soberana, cifrado E2E, relay ciego — de la forma más simple posible: abrís la app, agregás un contacto, mandás un mensaje.

Esto es, en los términos del propio Web5-Mesh, la **Fase 1** del proyecto: comunicación cifrada punto a punto sobre el motor de transporte y el Faro. Nada más, nada menos — pero probado por gente común, no solo por quien sabe compilar Go.

La meta de fondo es más grande que un chat: Web5-Mesh apunta a convertirse en un kernel de red completo, con su propia interfaz — una **superconsola** hecha en Flutter — donde chat, archivos, IA colaborativa y lo que venga en fases futuras conviven bajo la misma identidad soberana. XionChat es el primer paso concreto de esa interfaz, no un producto aparte.

---

## Faros públicos (para pruebas)

XionChat viene preconfigurado con dos faros públicos que funcionan como relays ciegos. No almacenan nada, no leen nada, solo reenvían.

| Faro | IP | Puertos | Protocolo |
|:---|:---|:---|:---|
| **Faro 1 (Argentina)** | `190.220.45.26` | `443` / `54321` | UDP / WSS (fallback) |
| **Faro 2 (Oracle)** | `155.136.55.87` | `443` / `54321` | UDP / WSS (fallback) |

**Lógica de conexión:**
1. Intenta UDP primero (puerto 443).
2. Si UDP falla, intenta WebSocket sobre TLS (puerto 443).
3. Si el faro 1 no responde, pasa al faro 2 automáticamente.

**Gate DID:** El faro solo responde a nodos con un `did:maia` válido. Los escáneres y bots que tocan el puerto sin un handshake firmado no reciben respuesta ni generan logs.

---

## Cómo funciona, en corto

```
┌─────────────┐        FFI         ┌──────────────────┐        UDP/WSS        ┌──────┐
│  XionChat   │ ◄────────────────► │  libxionia.so    │ ◄────────────────────►│ Faro │
│  (Flutter)  │                    │  (motor Web5)    │      cifrado E2E      │      │
└─────────────┘                    └──────────────────┘                       └──────┘
```

La UI en Flutter nunca toca la red directamente — le habla por FFI a una librería nativa compilada desde el propio código de Web5-Mesh, que es quien maneja identidad, cifrado, y la conexión al Faro.

---

## Qué tiene hoy (v1.0.1)

- **Cifrado E2E real**: Ed25519 para identidad y firmas, X25519 para intercambio de claves, ChaCha20-Poly1305 para el contenido.
- **Sin registro**: tu identidad (`did:maia:...`) se genera sola la primera vez que abrís la app.
- **Confianza explícita**: agregás un contacto compartiendo un paquete de claves públicas — vos decidís en quién confiar.
- **Relay ciego con Gate anti-bot**: el Faro no descifra nada y no guarda logs; exige handshake firmado antes de responder.
- **Transporte dual con fallback automático**: UDP primero, WebSocket sobre TLS si UDP no pasa.
- **Dos faros públicos con redundancia**: si uno cae, el otro sigue. La app conmuta automáticamente.
- **Chat efímero por diseño**: al cerrar una conversación, se borra. Grabación local opcional.
- **Grupos**: recepción y participación desde Android. Creación y administración solo desde la shell (`mesh shell`) — se resuelve en la próxima versión.

---

## Qué no tiene todavía (a propósito, no por olvido)

- **Persistencia robusta en background**: en Android 12, al deslizar de recientes, la app reconecta sola en ~20 segundos. En Android 15 ya persiste correctamente. Se sigue mejorando.
- **Grupos completos desde la UI**: la lógica está en Go, pero la pantalla de gestión de grupos en Flutter está pendiente.
- **Multi-dispositivo**: una identidad vive en un dispositivo. Sin sincronización entre dispositivos todavía.
- **Llamadas de voz/video**: planeadas para fases futuras.

La versión `v1.0.1` es estable para chat 1 a 1.

---

## Estado del proyecto

**v1.0.1 "Nebuchadnezzar" — Release estable**

Estable para chat 1 a 1 entre dos dispositivos con el Faro andando. Se usa y se prueba a diario. No es production-ready para entornos críticos, pero ya es funcional y soberano.

APK: [Releases](https://github.com/mamanga1/xionia-xtp/releases/tag/v1.0.1)
SHA256: `93dc2e6601c771cce7dfd44b8ed845f28cedb642f7481af18f8079ceae25c9d0`

---

## Compilar

Necesitás el motor de [Web5-Mesh](https://github.com/mamanga1/Web5-Mesh) para levantar tu propio Faro, y el Android NDK para compilar la librería nativa de este repo.

```bash
git clone https://github.com/mamanga1/xionia-xtp.git
cd xionia-xtp
go mod tidy
./build.sh android
cp android/jniLibs/arm64-v8a/libxionia.so xionchat_flutter/android/app/src/main/jniLibs/arm64-v8a/
cd xionchat_flutter
flutter pub get
flutter build apk --release
```

---

## Roadmap de plataformas

Hoy es Android. Si esto se estabiliza, la idea es no quedarse ahí:

- [x] Android (APK) — v1.0.1
- [ ] Windows
- [ ] Debian / Linux
- [ ] iOS

Nada de esto tiene fecha todavía — se publica cuando esté probado, no antes.

---

## Relación con Web5-Mesh

Este repo **no reimplementa** el protocolo — usa el motor de [Web5-Mesh](https://github.com/mamanga1/Web5-Mesh) tal cual, compilado como librería nativa. Toda la lógica de identidad, cifrado, ACL y transporte vive ahí; acá solo está la interfaz para usarlo desde Android. Si te interesa el protocolo en sí, el cliente de terminal, o levantar tu propio Faro, ese es el repo que tenés que mirar.

---

## Contribuir

Issues y PRs son bienvenidos. Si encontrás un bug de conexión, ayuda muchísimo si adjuntás el log de la app y, si podés reproducirlo, la salida del Faro en ese momento.

---

## Licencia

[AGPLv3 + Commons Clause](LICENSE) — Copyright (C) 2026 Fernando Martin Lopez.

---

<div align="center">
Hecho con orgullo desde Corrientes, Argentina. 🧉
</div>

