import 'dart:convert';
import 'dart:ffi';
import 'dart:io';
import 'package:ffi/ffi.dart';

// ─── tipos nativos ────────────────────────────────────────────────────────────
typedef _SetDataDirD      = void Function(Pointer<Utf8>);
typedef _ConnectFaroD     = Pointer<Utf8> Function(Pointer<Utf8>);
typedef _GetMyDIDD        = Pointer<Utf8> Function();
typedef _GetMyIdentityD   = Pointer<Utf8> Function();
typedef _ExportACLD       = Pointer<Utf8> Function();
typedef _ImportACLD       = void Function(Pointer<Utf8>);
typedef _ReloadACLD       = void Function();
typedef _GetContactsD     = Pointer<Utf8> Function();
typedef _SendChatD        = Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>);
typedef _PollMsgD         = Pointer<Utf8> Function();
typedef _ResetD           = void Function();
typedef _FreeD            = void Function(Pointer<Utf8>);
typedef _GetFaroD         = Pointer<Utf8> Function();
typedef _SetAliasD        = void Function(Pointer<Utf8>, Pointer<Utf8>);
typedef _RemoveAliasD     = void Function(Pointer<Utf8>);
// Grupos
typedef _CreateGroupD     = Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>);
typedef _GroupSendD       = Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>);
typedef _ListGroupsD      = Pointer<Utf8> Function();
typedef _GroupAddMemberD  = Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>);

/// Excepción específica para fallos al cargar/enlazar libxionia.so.
class XioniaLoadException implements Exception {
  final String message;
  XioniaLoadException(this.message);
  @override
  String toString() => message;
}

class Xionia {
  static DynamicLibrary? _lib;
  static String? _loadError;

  static _SetDataDirD?     _setDataDir;
  static _ConnectFaroD?    _connectFaro;
  static _GetMyDIDD?       _getMyDID;
  static _GetMyIdentityD?  _getMyIdentity;
  static _ExportACLD?      _exportACL;
  static _ImportACLD?      _importACL;
  static _ReloadACLD?      _reloadACL;
  static _GetContactsD?    _getContacts;
  static _SendChatD?       _sendChat;
  static _PollMsgD?        _pollMsg;
  static _ResetD?          _reset;
  static _FreeD?           _free;
  static _GetFaroD?        _getFaro;
  static _SetAliasD?       _setAlias;
  static _RemoveAliasD?    _removeAlias;
  // Grupos
  static _CreateGroupD?    _createGroup;
  static _GroupSendD?      _groupSend;
  static _ListGroupsD?     _listGroups;
  static _GroupAddMemberD? _groupAddMember;

  /// true si libxionia.so se cargó y todos los símbolos se resolvieron OK.
  static bool get ready => _lib != null;

  /// Mensaje de error si la carga falló.
  static String? get loadError => _loadError;

  /// Inicializar una vez al arrancar la app. No lanza — si falla, ready=false.
  static void init() {
    if (_lib != null || _loadError != null) return;
    try {
      final lib = Platform.isAndroid
          ? DynamicLibrary.open('libxionia.so')
          : DynamicLibrary.process();

      _setDataDir = lib
          .lookup<NativeFunction<Void Function(Pointer<Utf8>)>>('XioniaSetDataDir')
          .asFunction();
      _connectFaro = lib
          .lookup<NativeFunction<Pointer<Utf8> Function(Pointer<Utf8>)>>('XioniaConnectFaro')
          .asFunction();
      _getMyDID = lib
          .lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaGetMyDID')
          .asFunction();
      _getMyIdentity = lib
          .lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaGetMyIdentity')
          .asFunction();
      _exportACL = lib
          .lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaExportACLPacket')
          .asFunction();
      _importACL = lib
          .lookup<NativeFunction<Void Function(Pointer<Utf8>)>>('XioniaImportACLPacket')
          .asFunction();
      _reloadACL = lib
          .lookup<NativeFunction<Void Function()>>('XioniaReloadACL')
          .asFunction();
      _getContacts = lib
          .lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaGetContactsJSON')
          .asFunction();
      _sendChat = lib
          .lookup<NativeFunction<Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>)>>(
              'XioniaSendChat')
          .asFunction();
      _pollMsg = lib
          .lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaPollMessages')
          .asFunction();
      _reset = lib.lookup<NativeFunction<Void Function()>>('XioniaReset').asFunction();
      _free = lib
          .lookup<NativeFunction<Void Function(Pointer<Utf8>)>>('XioniaFreeString')
          .asFunction();
      _getFaro = lib
          .lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaGetFaroAddr')
          .asFunction();
      _setAlias = lib
          .lookup<NativeFunction<Void Function(Pointer<Utf8>, Pointer<Utf8>)>>('XioniaSetAlias')
          .asFunction();
      _removeAlias = lib
          .lookup<NativeFunction<Void Function(Pointer<Utf8>)>>('XioniaRemoveAlias')
          .asFunction();
      // Grupos
      _createGroup = lib
          .lookup<NativeFunction<Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>)>>(
              'XioniaCreateGroup')
          .asFunction();
      _groupSend = lib
          .lookup<NativeFunction<Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>)>>(
              'XioniaGroupSend')
          .asFunction();
      _listGroups = lib
          .lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaListGroups')
          .asFunction();
      _groupAddMember = lib
          .lookup<NativeFunction<Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>)>>(
              'XioniaGroupAddMember')
          .asFunction();

      _lib = lib;
    } catch (e) {
      _lib = null;
      _loadError = 'No se pudo cargar libxionia.so: $e\n'
          'Verificá que exista el .so para el ABI de este dispositivo '
          '(arm64-v8a, armeabi-v7a o x86_64) en android/app/src/main/jniLibs/.';
    }
  }

  static void _checkReady() {
    if (_lib == null) {
      throw XioniaLoadException(
          _loadError ?? 'Xionia no inicializado: llamá a Xionia.init() primero.');
    }
  }

  static void setDataDir(String path) {
    _checkReady();
    final p = path.toNativeUtf8();
    _setDataDir!(p);
    calloc.free(p);
  }

  static String connectFaro(String addr) {
    _checkReady();
    final p = addr.toNativeUtf8();
    final r = _connectFaro!(p);
    calloc.free(p);
    final s = r.toDartString();
    _free!(r);
    return s;
  }

  static String getMyDID() {
    _checkReady();
    final r = _getMyDID!();
    final s = r.toDartString();
    _free!(r);
    return s;
  }

  static String getMyIdentity() {
    _checkReady();
    final r = _getMyIdentity!();
    final s = r.toDartString();
    _free!(r);
    return s;
  }

  static String exportACL() {
    _checkReady();
    final r = _exportACL!();
    final s = r.toDartString();
    _free!(r);
    return s;
  }

  static void importACL(String packet) {
    _checkReady();
    final p = packet.toNativeUtf8();
    _importACL!(p);
    calloc.free(p);
  }

  static void reloadACL() {
    _checkReady();
    _reloadACL!();
  }

  static List<dynamic> getContacts() {
    _checkReady();
    final r = _getContacts!();
    final s = r.toDartString();
    _free!(r);
    return jsonDecode(s);
  }

  static String sendChat(String target, String msg) {
    _checkReady();
    final t = target.toNativeUtf8();
    final m = msg.toNativeUtf8();
    final r = _sendChat!(t, m);
    calloc.free(t);
    calloc.free(m);
    final s = r.toDartString();
    _free!(r);
    return s;
  }

  static List<dynamic> pollMessages() {
    _checkReady();
    final r = _pollMsg!();
    final s = r.toDartString();
    _free!(r);
    return jsonDecode(s);
  }

  static void reset() {
    _checkReady();
    _reset!();
  }

  static String getFaroAddr() {
    _checkReady();
    final r = _getFaro!();
    final s = r.toDartString();
    _free!(r);
    return s;
  }

  static void setAlias(String did, String alias) {
    _checkReady();
    final d = did.toNativeUtf8();
    final a = alias.toNativeUtf8();
    _setAlias!(d, a);
    calloc.free(d);
    calloc.free(a);
  }

  static void removeAlias(String did) {
    _checkReady();
    final d = did.toNativeUtf8();
    _removeAlias!(d);
    calloc.free(d);
  }

  // ─── Grupos ───────────────────────────────────────────────────────────────

  /// Crea un grupo nuevo. [alias] es la clave interna; [name] el nombre visible.
  /// Retorna "OK" o "ERROR: ...".
  static String createGroup(String alias, String name) {
    _checkReady();
    final a = alias.toNativeUtf8();
    final n = name.toNativeUtf8();
    final r = _createGroup!(a, n);
    calloc.free(a);
    calloc.free(n);
    final s = r.toDartString();
    _free!(r);
    return s;
  }

  /// Envía [message] a todos los miembros del grupo [alias].
  /// Retorna "OK:N" (N = enviados) o "ERROR: ...".
  static String groupSend(String alias, String message) {
    _checkReady();
    final a = alias.toNativeUtf8();
    final m = message.toNativeUtf8();
    final r = _groupSend!(a, m);
    calloc.free(a);
    calloc.free(m);
    final s = r.toDartString();
    _free!(r);
    return s;
  }

  /// Lista todos los grupos del nodo.
  /// Formato: List de Map con alias, name, members (List<String>), admin.
  static List<dynamic> listGroups() {
    _checkReady();
    final r = _listGroups!();
    final s = r.toDartString();
    _free!(r);
    return jsonDecode(s);
  }

  /// Agrega [targetDID] al grupo [alias] (solo el admin puede hacerlo).
  /// Retorna "OK" o "ERROR: ...".
  static String groupAddMember(String alias, String targetDID) {
    _checkReady();
    final a = alias.toNativeUtf8();
    final t = targetDID.toNativeUtf8();
    final r = _groupAddMember!(a, t);
    calloc.free(a);
    calloc.free(t);
    final s = r.toDartString();
    _free!(r);
    return s;
  }
}
