# Jami+++ v0.1 — Template para el Albañil

> **Comandante dice:** "El puente ya está. Vos solo cambiás la UI."

## 🎯 Qué hace este template

Este repo tiene todo listo para que un albañil (desarrollador C++/Qt) integre XionIA Faraday como motor de mensajería en una app Android, usando **Jami como esqueleto de UI**.

## 📁 Estructura

```
jami-plus-plus-template/
├── xionia-lib/
│   └── mobile.go          ← MOTOR GO (NO TOCAR)
│                            Vos lo compilás, el albañil lo recibe como .so
├── bridge/
│   └── jami_bridge.h      ← PUENTE C++ (NO TOCAR)
│                            Wrapper para que el albañil use funciones Go
│                            desde C++ sin saber que existe Go
├── ui/qml/
│   └── MainView.qml       ← UI (EL ALBAÑIL TOCA ESTO)
│                            Botones, chat, dialogs — todo en QML
├── android/
│   └── jniLibs/           ← Acá va libxionia.so compilado
└── docs/
    └── README.md          ← Estás acá
```

## 🔧 Paso 1: Vos compilás XionIA (Go)

```bash
# En tu Xeon
cd xionia-lib/

# Para Android (arm64)
GOOS=android GOARCH=arm64 CGO_ENABLED=1   go build -buildmode=c-shared -o libxionia.so mobile.go

# Para Linux desktop
# go build -buildmode=c-shared -o libxionia.so mobile.go

# Genera:
# - libxionia.so  (la librería)
# - libxionia.h   (headers automáticos de Go)
```

Copiá `libxionia.so` a `android/jniLibs/arm64-v8a/`.

## 🔨 Paso 2: Albañil — Integrar en Jami

### 2.1 Copiar archivos al repo de Jami

```bash
cd jami-android/

# Copiar bridge
cp /ruta/jami-plus-plus-template/bridge/jami_bridge.h    src/client/jni/xionia_bridge.h

# Copiar librería
cp /ruta/jami-plus-plus-template/android/jniLibs/arm64-v8a/libxionia.so    app/src/main/jniLibs/arm64-v8a/
```

### 2.2 Modificar build.gradle de Jami

Agregar en `app/build.gradle`:

```gradle
android {
    sourceSets {
        main {
            jniLibs.srcDirs = ['src/main/jniLibs']
        }
    }
}
```

### 2.3 Modificar main.cpp de Jami

En `src/client/main.cpp`, agregar al inicio:

```cpp
#include "xionia_bridge.h"

// Al arrancar la app:
int main(int argc, char *argv[]) {
    QGuiApplication app(argc, argv);

    // Exponer Xionia al QML
    QJSEngine::setObjectOwnership(&Xionia, QQmlEngine::CppOwnership);
    engine.rootContext()->setContextProperty("Xionia", new XioniaWrapper());

    // ... resto del main de Jami
}
```

### 2.4 Reemplazar la UI de Jami

Copiar `ui/qml/MainView.qml` a `src/client/qml/` de Jami.

**El albañil solo toca este archivo.** Los botones ya están cableados:
- 🔴 RESET → llama a `Xionia.reset()`
- 📤 COMPARTIR → llama a `Xionia.exportACLPacket()`
- ➕ AGREGAR → llama a `Xionia.importACLPacket(packet)`
- 🌐 FARO → llama a `Xionia.connectFaro(addr)`
- 💬 CHAT → llama a `Xionia.sendChat(target, msg)`

## 🧪 Paso 3: Compilar y probar

```bash
cd jami-android
./gradlew assembleDebug

# Instalar en Android
adb install app/build/outputs/apk/debug/app-debug.apk
```

## 🐛 Si algo falla

| Problema | Solución |
|----------|----------|
| `libxionia.so not found` | Verificar que está en `jniLibs/arm64-v8a/` |
| Símbolos no resueltos | Recompilar con `go build -buildmode=c-shared` |
| Crash al iniciar | Verificar que `XioniaFreeString` se llama después de cada `Xionia*()` que devuelve `char*` |
| UI no carga | Verificar que `engine.rootContext()->setContextProperty("Xionia", ...)` está antes de `engine.load()` |

## 📞 Contacto

Si el albañil se atora en algo que no sea la UI (QML), no toca el puente. Llama al Comandante. 🦾

---

## 🧉 La Posta

```
Vos (Go)          → Compilás mobile.go → libxionia.so
                    ↓
Albañil (C++/QML) → Copia jami_bridge.h + libxionia.so
                    → Modifica 3 archivos de Jami
                    → Toca SOLO MainView.qml
                    → Compila APK
```

**Tiempo real del albañil: 3-5 días.**
**Tiempo tuyo: 1 día (compilar la librería).**
