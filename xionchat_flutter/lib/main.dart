import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:path_provider/path_provider.dart';
import 'package:permission_handler/permission_handler.dart';
import 'flutter_bridge.dart';

// ============================================================================
// CONSTANTES
// ============================================================================
const _kServiceChannel = 'com.xionia/service';
const _kMsgChannelId   = 'xionia_messages';
const _kMsgChannelName = 'Mensajes XionChat';

// ============================================================================
// NOTIFICACIONES LOCALES
// Instancia global — se inicializa una vez en _HomeScreenState._init()
// y se usa desde MessageBus._tick() para notificar mensajes en background.
// ============================================================================
final _notifPlugin = FlutterLocalNotificationsPlugin();

Future<void> _initNotifications() async {
  await _notifPlugin.initialize(
    const InitializationSettings(
      android: AndroidInitializationSettings('@mipmap/ic_launcher'),
    ),
  );
  final androidImpl = _notifPlugin
      .resolvePlatformSpecificImplementation<AndroidFlutterLocalNotificationsPlugin>();
  await androidImpl?.createNotificationChannel(
    const AndroidNotificationChannel(
      _kMsgChannelId,
      _kMsgChannelName,
      description: 'Mensajes entrantes cifrados E2E',
      importance: Importance.high,
    ),
  );
}

void _showNotification(String sender, String text) {
  _notifPlugin.show(
    DateTime.now().millisecondsSinceEpoch ~/ 1000,
    '💬 $sender',
    text,
    const NotificationDetails(
      android: AndroidNotificationDetails(
        _kMsgChannelId,
        _kMsgChannelName,
        importance: Importance.high,
        priority: Priority.high,
      ),
    ),
  );
}

// ============================================================================
// MESSAGE BUS
// Modelo idéntico al v0.2-beta: UN SOLO timer en el isolate principal,
// sondea XioniaPollMessages() directamente (Go vive en el mismo proceso).
// No hay flutter_background_service, no hay segundo isolate, no hay
// invoke() entre procesos — la cola Go la lee un único caller.
// ============================================================================
class MessageBus {
  MessageBus._();
  static final MessageBus instance = MessageBus._();

  final _controller = StreamController<String>.broadcast();
  Stream<String> get stream => _controller.stream;

  Timer? _timer;

  // Qué chat tiene el usuario abierto ahora mismo (para no notificar
  // mensajes que ya está viendo en pantalla).
  String? activeChatDid;
  String? activeChatAlias;

  // Buffer circular en RAM: sobrevive a cerrar/reabrir la pantalla de
  // chat dentro de la misma sesión de la app, pero se pierde si el
  // proceso muere (sigue siendo "efímero" en ese sentido).
  final List<_BusEntry> _recent = [];
  static const _recentCap = 500;

  void _remember(_BusEntry e) {
    _recent.add(e);
    if (_recent.length > _recentCap) _recent.removeAt(0);
  }

  void addSent(String did, String displayText) {
    _remember(_BusEntry(mine: true, did: did, display: displayText));
  }

  List<String> recentFor(String did, String alias) {
    return _recent
        .where((e) => e.mine
            ? e.did == did
            : (e.display.contains(alias) || e.display.contains(did)))
        .map((e) => e.display)
        .toList();
  }

  void start() {
    if (_timer != null) return;
    _timer = Timer.periodic(const Duration(seconds: 2), (_) => _tick());
  }

  void stop() {
    _timer?.cancel();
    _timer = null;
  }

  /// Sondea ya mismo sin esperar al próximo tick (útil al volver del
  /// background para no perder mensajes que llegaron mientras estábamos
  /// pausados).
  void pollNow() => _tick();

  void _tick() {
    if (!Xionia.ready) return;
    List<dynamic> polled;
    try {
      polled = Xionia.pollMessages();
    } catch (_) {
      return;
    }
    for (final m in polled) {
      final s = m.toString();
      _remember(_BusEntry(mine: false, did: null, display: s));
      _controller.add(s);

      // Notificación local solo si el chat correspondiente NO está abierto
      final isActiveChat = activeChatDid != null &&
          (s.contains(activeChatDid!) ||
              (activeChatAlias != null && s.contains(activeChatAlias!)));
      if (!isActiveChat) {
        final idx = s.indexOf(': ');
        final sender = idx == -1 ? s : s.substring(0, idx);
        final text   = idx == -1 ? '' : s.substring(idx + 2).replaceFirst('CHAT:', '');
        _showNotification(sender, text);
      }
    }
  }
}

class _BusEntry {
  final bool mine;
  final String? did;
  final String display;
  _BusEntry({required this.mine, required this.did, required this.display});
}

// ============================================================================
// MAIN
// ============================================================================
void main() => runApp(const XionChatApp());

class XionChatApp extends StatelessWidget {
  const XionChatApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'XionChat',
      debugShowCheckedModeBanner: false,
      theme: ThemeData.dark().copyWith(
        colorScheme: ColorScheme.dark(
          primary:   const Color(0xFF128C7E),
          secondary: const Color(0xFF25D366),
          surface:   const Color(0xFF121212),
        ),
        scaffoldBackgroundColor: const Color(0xFF0B141A),
      ),
      home: const HomeScreen(),
    );
  }
}

// ============================================================================
// HOME SCREEN
// ============================================================================
class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key});
  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> with WidgetsBindingObserver {
  String myDID       = '...';
  String faroStatus  = 'off';
  List<dynamic> contacts = [];
  String? _loadError;
  String _lastFaroAddr = '';

  // MethodChannel al XioniaService.kt nativo (foreground service Kotlin).
  static const _svcChannel = MethodChannel(_kServiceChannel);

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _init();
  }

  Future<void> _init() async {
    Xionia.init();
    if (!Xionia.ready) {
      setState(() => _loadError = Xionia.loadError);
      return;
    }

    // Permisos: notificaciones (Android 13+) y exención de batería.
    if (Platform.isAndroid) {
      try {
        if (await Permission.notification.isDenied) {
          await Permission.notification.request();
        }
        if (await Permission.ignoreBatteryOptimizations.isDenied) {
          await Permission.ignoreBatteryOptimizations.request();
        }
      } catch (_) {}
    }

    final dir = await getApplicationDocumentsDirectory();
    Xionia.setDataDir(dir.path);

    await _initNotifications();

    // Arrancar el foreground service nativo (wakelock, notificación fija).
    if (Platform.isAndroid) {
      try {
        await _svcChannel.invokeMethod('startService');
      } catch (_) {}
    }

    // Arrancar el bus de mensajes (timer en este isolate).
    MessageBus.instance.start();

    setState(() {
      myDID      = Xionia.getMyDID();
      faroStatus = Xionia.getFaroAddr();
      final raw  = faroStatus.split(' (').first.trim();
      if (raw.isNotEmpty && raw != 'off') _lastFaroAddr = raw;
      _loadContacts();
    });
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed && Xionia.ready) {
      // Volvemos del background: forzar poll inmediato y refrescar UI.
      MessageBus.instance.pollNow();
      setState(() => faroStatus = Xionia.getFaroAddr());
    }
  }

  void _loadContacts() {
    setState(() => contacts = Xionia.getContacts());
  }

  // --------------------------------------------------------------------------
  // CONECTAR AL FARO
  // Corre en Isolate.run() para no bloquear el hilo de UI (el handshake
  // TLS de WSS puede tardar hasta 5s).
  // --------------------------------------------------------------------------
  void _connect() {
    showDialog(
      context: context,
      builder: (_) {
        final ctrl = TextEditingController(text: _lastFaroAddr.isNotEmpty ? _lastFaroAddr : '190.220.45.26:54321');
        bool connecting = false;
        return StatefulBuilder(
          builder: (context, setDialogState) => AlertDialog(
            backgroundColor: const Color(0xFF1F2C34),
            title: const Text('Conectar al Faro', style: TextStyle(color: Colors.white)),
            content: TextField(
              controller: ctrl,
              enabled: !connecting,
              style: const TextStyle(color: Colors.white),
              decoration: const InputDecoration(
                labelText: 'IP:PUERTO',
                labelStyle: TextStyle(color: Colors.grey),
                enabledBorder: UnderlineInputBorder(
                    borderSide: BorderSide(color: Color(0xFF128C7E))),
              ),
            ),
            actions: [
              TextButton(
                onPressed: connecting ? null : () => Navigator.pop(context),
                child: const Text('Cancelar', style: TextStyle(color: Colors.grey)),
              ),
              TextButton(
                onPressed: connecting
                    ? null
                    : () async {
                        setDialogState(() => connecting = true);
                        final addr = ctrl.text.trim();
                        // Isolate.run: crea un isolate temporario solo para
                        // esta llamada bloqueante y lo destruye al terminar.
                        // No es el mismo problema que flutter_background_service
                        // (que crea un isolate PERMANENTE con su propia copia
                        // del estado Go). Acá el isolate solo hace la llamada
                        // FFI bloqueante y devuelve el string resultado — Go
                        // sigue siendo un único proceso compartido.
                        final r = await Future(() => Xionia.connectFaro(addr));
                        _lastFaroAddr = addr;
                        if (context.mounted) Navigator.pop(context);
                        if (mounted) {
                          ScaffoldMessenger.of(context).showSnackBar(SnackBar(
                            content: Text(r),
                            backgroundColor: const Color(0xFF1F2C34),
                          ));
                          setState(() => faroStatus = Xionia.getFaroAddr());
                        }
                      },
                child: connecting
                    ? const SizedBox(
                        width: 16, height: 16,
                        child: CircularProgressIndicator(
                            strokeWidth: 2, color: Color(0xFF25D366)),
                      )
                    : const Text('Conectar', style: TextStyle(color: Color(0xFF25D366))),
              ),
            ],
          ),
        );
      },
    );
  }

  // --------------------------------------------------------------------------
  // IDENTIDAD
  // --------------------------------------------------------------------------
  void _showMyIdentity() {
    final id = Xionia.getMyIdentity();
    showDialog(
      context: context,
      builder: (_) => AlertDialog(
        backgroundColor: const Color(0xFF1F2C34),
        title: const Text('Mi Identidad', style: TextStyle(color: Colors.white)),
        content: SingleChildScrollView(
          child: SelectableText(id,
              style: const TextStyle(
                  color: Colors.white, fontFamily: 'monospace', fontSize: 12)),
        ),
        actions: [
          TextButton(
            onPressed: () {
              Clipboard.setData(ClipboardData(text: id));
              Navigator.pop(context);
              ScaffoldMessenger.of(context).showSnackBar(
                const SnackBar(content: Text('Copiado'), duration: Duration(seconds: 1)),
              );
            },
            child: const Text('Copiar', style: TextStyle(color: Color(0xFF25D366))),
          ),
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Cerrar', style: TextStyle(color: Colors.grey)),
          ),
        ],
      ),
    );
  }

  // --------------------------------------------------------------------------
  // COMPARTIR ACL
  // --------------------------------------------------------------------------
  void _shareACL() {
    final packet = Xionia.exportACL();
    showDialog(
      context: context,
      builder: (_) => AlertDialog(
        backgroundColor: const Color(0xFF1F2C34),
        title: const Text('Compartir Red', style: TextStyle(color: Colors.white)),
        content: SingleChildScrollView(
          child: SelectableText(packet,
              style: const TextStyle(
                  color: Colors.white, fontFamily: 'monospace', fontSize: 13)),
        ),
        actions: [
          TextButton(
            onPressed: () {
              Clipboard.setData(ClipboardData(text: packet));
              Navigator.pop(context);
              ScaffoldMessenger.of(context).showSnackBar(
                const SnackBar(
                    content: Text('Copiado al portapapeles'),
                    duration: Duration(seconds: 1)),
              );
            },
            child: const Text('Copiar', style: TextStyle(color: Color(0xFF25D366))),
          ),
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Cerrar', style: TextStyle(color: Colors.grey)),
          ),
        ],
      ),
    );
  }

  // --------------------------------------------------------------------------
  // AGREGAR CONTACTO
  // --------------------------------------------------------------------------
  void _addContact() {
    showDialog(
      context: context,
      builder: (_) {
        final ctrl = TextEditingController();
        return AlertDialog(
          backgroundColor: const Color(0xFF1F2C34),
          title: const Text('Agregar Contacto', style: TextStyle(color: Colors.white)),
          content: SizedBox(
            width: double.maxFinite,
            child: TextField(
              controller: ctrl,
              maxLines: null,
              minLines: 3,
              keyboardType: TextInputType.multiline,
              style: const TextStyle(color: Colors.white, fontSize: 13),
              decoration: const InputDecoration(
                hintText: 'acl import did:maia:... pubEd pubX',
                hintStyle: TextStyle(color: Colors.grey, fontSize: 12),
                border: OutlineInputBorder(
                    borderSide: BorderSide(color: Color(0xFF128C7E))),
              ),
            ),
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(context),
              child: const Text('Cancelar', style: TextStyle(color: Colors.grey)),
            ),
            TextButton(
              onPressed: () {
                Xionia.importACL(ctrl.text);
                Xionia.reloadACL();
                _loadContacts();
                Navigator.pop(context);
                ScaffoldMessenger.of(context).showSnackBar(
                  const SnackBar(
                      content: Text('Contacto agregado'),
                      duration: Duration(seconds: 1)),
                );
              },
              child: const Text('Agregar', style: TextStyle(color: Color(0xFF25D366))),
            ),
          ],
        );
      },
    );
  }

  // --------------------------------------------------------------------------
  // ALIAS
  // --------------------------------------------------------------------------
  void _setAliasDialog(String did, String currentAlias) {
    final ctrl = TextEditingController(text: currentAlias);
    showDialog(
      context: context,
      builder: (_) => AlertDialog(
        backgroundColor: const Color(0xFF1F2C34),
        title: const Text('Poner Alias', style: TextStyle(color: Colors.white)),
        content: TextField(
          controller: ctrl,
          style: const TextStyle(color: Colors.white),
          decoration: const InputDecoration(
            hintText: 'Nombre del contacto',
            hintStyle: TextStyle(color: Colors.grey),
            enabledBorder: UnderlineInputBorder(
                borderSide: BorderSide(color: Color(0xFF128C7E))),
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Cancelar', style: TextStyle(color: Colors.grey)),
          ),
          TextButton(
            onPressed: () {
              if (ctrl.text.trim().isNotEmpty) {
                Xionia.setAlias(did, ctrl.text.trim());
              } else {
                Xionia.removeAlias(did);
              }
              Xionia.reloadACL();
              _loadContacts();
              Navigator.pop(context);
            },
            child: const Text('Guardar', style: TextStyle(color: Color(0xFF25D366))),
          ),
        ],
      ),
    );
  }

  // --------------------------------------------------------------------------
  // RESET
  // --------------------------------------------------------------------------
  void _showResetMenu() {
    showModalBottomSheet(
      context: context,
      backgroundColor: const Color(0xFF1F2C34),
      builder: (_) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            ListTile(
              leading: const Icon(Icons.delete_forever, color: Colors.red),
              title: const Text('RESET TOTAL (borra todo)',
                  style: TextStyle(color: Colors.red)),
              onTap: () async {
                Navigator.pop(context);
                // Parar el bus antes del reset para que no haya ningún
                // poll en vuelo mientras Go limpia los archivos.
                MessageBus.instance.stop();
                Xionia.reset();
                // Dar tiempo a Go para terminar de limpiar antes de
                // releer el estado desde Dart.
                await Future.delayed(const Duration(milliseconds: 500));
                // Reiniciar el bus limpio.
                MessageBus.instance.start();
                if (mounted) {
                  setState(() {
                    myDID      = Xionia.getMyDID();
                    contacts   = [];
                    faroStatus = 'off';
                    _lastFaroAddr = '';
                  });
                }
              },
            ),
            ListTile(
              leading: const Icon(Icons.close, color: Colors.grey),
              title: const Text('Cancelar', style: TextStyle(color: Colors.white)),
              onTap: () => Navigator.pop(context),
            ),
          ],
        ),
      ),
    );
  }

  void _openChat(String did, String displayName) {
    Navigator.push(
      context,
      MaterialPageRoute(builder: (_) => ChatScreen(did: did, alias: displayName)),
    );
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    MessageBus.instance.stop();
    super.dispose();
  }

  // --------------------------------------------------------------------------
  // BUILD
  // --------------------------------------------------------------------------
  @override
  Widget build(BuildContext context) {
    if (_loadError != null) {
      return Scaffold(
        backgroundColor: const Color(0xFF0B141A),
        body: Center(
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                const Icon(Icons.error_outline, color: Colors.redAccent, size: 48),
                const SizedBox(height: 16),
                const Text('No se pudo iniciar el motor XionIA',
                    style: TextStyle(color: Colors.white, fontSize: 16)),
                const SizedBox(height: 8),
                Text(_loadError!,
                    style: const TextStyle(color: Colors.grey, fontSize: 12),
                    textAlign: TextAlign.center),
              ],
            ),
          ),
        ),
      );
    }

    return Scaffold(
      backgroundColor: const Color(0xFF0B141A),
      appBar: AppBar(
        backgroundColor: const Color(0xFF1F2C34),
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text('XionChat', style: TextStyle(fontSize: 18)),
            Text(
              myDID.length > 20 ? '${myDID.substring(0, 20)}...' : myDID,
              style: const TextStyle(fontSize: 12, color: Colors.grey),
            ),
          ],
        ),
        actions: [
          IconButton(
            icon: Icon(
              faroStatus.contains('off') ? Icons.cloud_off : Icons.cloud_done,
              color: const Color(0xFF25D366),
            ),
            onPressed: _connect,
          ),
          PopupMenuButton<String>(
            color: const Color(0xFF1F2C34),
            onSelected: (v) {
              if (v == 'identity') _showMyIdentity();
              if (v == 'share') _shareACL();
              if (v == 'reset') _showResetMenu();
            },
            itemBuilder: (_) => [
              const PopupMenuItem(
                  value: 'identity',
                  child: Text('Mi Identidad', style: TextStyle(color: Colors.white))),
              const PopupMenuItem(
                  value: 'share',
                  child: Text('Compartir Red', style: TextStyle(color: Colors.white))),
              const PopupMenuItem(
                  value: 'reset',
                  child: Text('RESET', style: TextStyle(color: Colors.red))),
            ],
          ),
        ],
      ),
      body: contacts.isEmpty
          ? const Center(
              child: Text('Sin contactos. Agregá uno con el +',
                  style: TextStyle(color: Colors.grey)))
          : ListView.builder(
              itemCount: contacts.length,
              itemBuilder: (_, i) {
                final c       = contacts[i];
                final did     = c['did'].toString();
                final alias   = (c['alias'] ?? '').toString();
                final display = alias.isNotEmpty ? alias : did;
                return ListTile(
                  leading: CircleAvatar(
                    backgroundColor: const Color(0xFF128C7E),
                    child: Text(display[0].toUpperCase(),
                        style: const TextStyle(color: Colors.white)),
                  ),
                  title: Text(display, style: const TextStyle(color: Colors.white)),
                  subtitle: Text(did,
                      style: const TextStyle(color: Colors.grey, fontSize: 12),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis),
                  onTap: () => _openChat(did, display),
                  onLongPress: () => _setAliasDialog(did, alias),
                );
              },
            ),
      floatingActionButton: FloatingActionButton(
        backgroundColor: const Color(0xFF25D366),
        onPressed: _addContact,
        child: const Icon(Icons.person_add, color: Colors.white),
      ),
    );
  }
}

// ============================================================================
// CHAT SCREEN
// ============================================================================
class ChatScreen extends StatefulWidget {
  final String did;
  final String alias;
  const ChatScreen({super.key, required this.did, required this.alias});

  @override
  State<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends State<ChatScreen> {
  final _ctrl       = TextEditingController();
  final _msgs       = <String>[];
  final _scrollCtrl = ScrollController();
  StreamSubscription<String>? _sub;
  bool _recording = false;

  String get _historyFileName {
    final safe = widget.did.replaceAll(RegExp(r'[^A-Za-z0-9]'), '_');
    return 'chat_$safe.json';
  }

  Future<File> _historyFile() async {
    final dir = await getApplicationDocumentsDirectory();
    return File('${dir.path}/$_historyFileName');
  }

  @override
  void initState() {
    super.initState();
    MessageBus.instance.activeChatDid   = widget.did;
    MessageBus.instance.activeChatAlias = widget.alias;

    _loadHistory();

    _sub = MessageBus.instance.stream.listen((m) {
      if (m.contains(widget.alias) || m.contains(widget.did)) {
        setState(() => _msgs.add(m));
        _saveIfRecording();
        _scrollToEnd();
      }
    });
  }

  Future<void> _loadHistory() async {
    var fromFile = <String>[];
    try {
      final f = await _historyFile();
      if (await f.exists()) {
        final data = jsonDecode(await f.readAsString()) as List<dynamic>;
        fromFile    = data.map((e) => e.toString()).toList();
        _recording  = true;
      }
    } catch (_) {}

    final fromRam = MessageBus.instance.recentFor(widget.did, widget.alias);
    final merged  = [...fromFile];
    for (final m in fromRam) {
      if (!merged.contains(m)) merged.add(m);
    }
    if (merged.isNotEmpty) {
      setState(() => _msgs.addAll(merged));
      _scrollToEnd();
    }
  }

  Future<void> _saveIfRecording() async {
    if (!_recording) return;
    try {
      final f = await _historyFile();
      await f.writeAsString(jsonEncode(_msgs));
    } catch (_) {}
  }

  Future<void> _toggleRecording() async {
    setState(() => _recording = !_recording);
    if (_recording) {
      await _saveIfRecording();
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
              content: Text('Grabando conversación'),
              duration: Duration(seconds: 1)),
        );
      }
    } else {
      try {
        final f = await _historyFile();
        if (await f.exists()) await f.delete();
      } catch (_) {}
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
              content: Text('Chat efímero: se borra al salir'),
              duration: Duration(seconds: 1)),
        );
      }
    }
  }

  void _scrollToEnd() {
    Future.delayed(const Duration(milliseconds: 100), () {
      if (_scrollCtrl.hasClients) {
        _scrollCtrl.animateTo(
          _scrollCtrl.position.maxScrollExtent,
          duration: const Duration(milliseconds: 200),
          curve: Curves.easeOut,
        );
      }
    });
  }

  void _send() {
    if (_ctrl.text.isEmpty) return;
    final text    = _ctrl.text;
    final r       = Xionia.sendChat(widget.did, text);
    final display = 'Tú: $text';
    setState(() => _msgs.add(display));
    MessageBus.instance.addSent(widget.did, display);
    _saveIfRecording();
    _ctrl.clear();
    _scrollToEnd();
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(
      content: Text(r),
      duration: const Duration(seconds: 1),
      backgroundColor: const Color(0xFF1F2C34),
    ));
  }

  @override
  void dispose() {
    if (MessageBus.instance.activeChatDid == widget.did) {
      MessageBus.instance.activeChatDid   = null;
      MessageBus.instance.activeChatAlias = null;
    }
    _sub?.cancel();
    _scrollCtrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: const Color(0xFF0B141A),
      appBar: AppBar(
        backgroundColor: const Color(0xFF1F2C34),
        title: Text(widget.alias, style: const TextStyle(color: Colors.white)),
        actions: [
          IconButton(
            tooltip: _recording
                ? 'Grabando (tocá para volver a efímero)'
                : 'Chat efímero (tocá para grabar)',
            icon: Icon(
              _recording ? Icons.fiber_manual_record : Icons.radio_button_unchecked,
              color: _recording ? Colors.redAccent : Colors.grey,
            ),
            onPressed: _toggleRecording,
          ),
        ],
      ),
      body: Column(
        children: [
          Expanded(
            child: ListView.builder(
              controller: _scrollCtrl,
              itemCount: _msgs.length,
              itemBuilder: (_, i) {
                final isMe = _msgs[i].startsWith('Tú:');
                final text = _msgs[i]
                    .replaceFirst('Tú: ', '')
                    .replaceFirst(RegExp(r'^[^:]+:\s*'), '')
                    .replaceFirst(RegExp(r'^CHAT:\s*'), '');
                return Align(
                  alignment: isMe ? Alignment.centerRight : Alignment.centerLeft,
                  child: Container(
                    margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
                    padding: const EdgeInsets.all(12),
                    constraints: BoxConstraints(
                        maxWidth: MediaQuery.of(context).size.width * 0.75),
                    decoration: BoxDecoration(
                      color: isMe
                          ? const Color(0xFF005C4B)
                          : const Color(0xFF1F2C34),
                      borderRadius: BorderRadius.circular(8),
                    ),
                    child: Text(text, style: const TextStyle(color: Colors.white)),
                  ),
                );
              },
            ),
          ),
          Container(
            color: const Color(0xFF1F2C34),
            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
            child: Row(
              children: [
                Expanded(
                  child: TextField(
                    controller: _ctrl,
                    style: const TextStyle(color: Colors.white),
                    decoration: const InputDecoration(
                      hintText: 'Mensaje...',
                      hintStyle: TextStyle(color: Colors.grey),
                      border: InputBorder.none,
                    ),
                    onSubmitted: (_) => _send(),
                  ),
                ),
                IconButton(
                  icon: const Icon(Icons.send, color: Color(0xFF25D366)),
                  onPressed: _send,
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}
