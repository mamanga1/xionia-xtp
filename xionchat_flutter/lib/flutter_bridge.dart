import 'dart:convert';
import 'dart:ffi';
import 'dart:io';
import 'package:ffi/ffi.dart';

final DynamicLibrary _lib = Platform.isAndroid
    ? DynamicLibrary.open('libxionia.so')
    : DynamicLibrary.process();

typedef _SetDataDirD = void Function(Pointer<Utf8>);
typedef _ConnectFaroD = Pointer<Utf8> Function(Pointer<Utf8>);
typedef _GetMyDIDD = Pointer<Utf8> Function();
typedef _GetMyIdentityD = Pointer<Utf8> Function();
typedef _ExportACLD = Pointer<Utf8> Function();
typedef _ImportACLD = void Function(Pointer<Utf8>);
typedef _ReloadACLD = void Function();
typedef _GetContactsD = Pointer<Utf8> Function();
typedef _SendChatD = Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>);
typedef _PollMsgD = Pointer<Utf8> Function();
typedef _ResetD = void Function();
typedef _FreeD = void Function(Pointer<Utf8>);
typedef _GetFaroD = Pointer<Utf8> Function();
typedef _SetAliasD = void Function(Pointer<Utf8>, Pointer<Utf8>);
typedef _RemoveAliasD = void Function(Pointer<Utf8>);

final _setDataDir = _lib.lookup<NativeFunction<Void Function(Pointer<Utf8>)>>('XioniaSetDataDir').asFunction<_SetDataDirD>();
final _connectFaro = _lib.lookup<NativeFunction<Pointer<Utf8> Function(Pointer<Utf8>)>>('XioniaConnectFaro').asFunction<_ConnectFaroD>();
final _getMyDID = _lib.lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaGetMyDID').asFunction<_GetMyDIDD>();
final _getMyIdentity = _lib.lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaGetMyIdentity').asFunction<_GetMyIdentityD>();
final _exportACL = _lib.lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaExportACLPacket').asFunction<_ExportACLD>();
final _importACL = _lib.lookup<NativeFunction<Void Function(Pointer<Utf8>)>>('XioniaImportACLPacket').asFunction<_ImportACLD>();
final _reloadACL = _lib.lookup<NativeFunction<Void Function()>>('XioniaReloadACL').asFunction<_ReloadACLD>();
final _getContacts = _lib.lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaGetContactsJSON').asFunction<_GetContactsD>();
final _sendChat = _lib.lookup<NativeFunction<Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>)>>('XioniaSendChat').asFunction<_SendChatD>();
final _pollMsg = _lib.lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaPollMessages').asFunction<_PollMsgD>();
final _reset = _lib.lookup<NativeFunction<Void Function()>>('XioniaReset').asFunction<_ResetD>();
final _free = _lib.lookup<NativeFunction<Void Function(Pointer<Utf8>)>>('XioniaFreeString').asFunction<_FreeD>();
final _getFaro = _lib.lookup<NativeFunction<Pointer<Utf8> Function()>>('XioniaGetFaroAddr').asFunction<_GetFaroD>();
final _setAlias = _lib.lookup<NativeFunction<Void Function(Pointer<Utf8>, Pointer<Utf8>)>>('XioniaSetAlias').asFunction<_SetAliasD>();
final _removeAlias = _lib.lookup<NativeFunction<Void Function(Pointer<Utf8>)>>('XioniaRemoveAlias').asFunction<_RemoveAliasD>();

class Xionia {
  static void setDataDir(String path) {
    final p = path.toNativeUtf8(); _setDataDir(p); calloc.free(p);
  }
  static String connectFaro(String addr) {
    final p = addr.toNativeUtf8(); final r = _connectFaro(p); calloc.free(p);
    final s = r.toDartString(); _free(r); return s;
  }
  static String getMyDID() {
    final r = _getMyDID(); final s = r.toDartString(); _free(r); return s;
  }
  static String getMyIdentity() {
    final r = _getMyIdentity(); final s = r.toDartString(); _free(r); return s;
  }
  static String exportACL() {
    final r = _exportACL(); final s = r.toDartString(); _free(r); return s;
  }
  static void importACL(String packet) {
    final p = packet.toNativeUtf8(); _importACL(p); calloc.free(p);
  }
  static void reloadACL() => _reloadACL();
  static List<dynamic> getContacts() {
    final r = _getContacts(); final s = r.toDartString(); _free(r); return jsonDecode(s);
  }
  static String sendChat(String target, String msg) {
    final t = target.toNativeUtf8(); final m = msg.toNativeUtf8();
    final r = _sendChat(t, m); calloc.free(t); calloc.free(m);
    final s = r.toDartString(); _free(r); return s;
  }
  static List<dynamic> pollMessages() {
    final r = _pollMsg(); final s = r.toDartString(); _free(r); return jsonDecode(s);
  }
  static void reset() => _reset();
  static String getFaroAddr() {
    final r = _getFaro(); final s = r.toDartString(); _free(r); return s;
  }
  static void setAlias(String did, String alias) {
    final d = did.toNativeUtf8(); final a = alias.toNativeUtf8();
    _setAlias(d, a); calloc.free(d); calloc.free(a);
  }
  static void removeAlias(String did) {
    final d = did.toNativeUtf8(); _removeAlias(d); calloc.free(d);
  }
}
