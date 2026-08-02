import 'dart:isolate';
import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:ui';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_background_service/flutter_background_service.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:path_provider/path_provider.dart';
import 'package:permission_handler/permission_handler.dart';
import 'flutter_bridge.dart';

const _serviceChannelId = 'xionia_mesh';
const _msgChannelId = 'xionia_messages';

// ============================================================================
// SERVICIO DE BACKGROUND
// Isolate propio, independiente de la Activity. Es el ÚNICO que sondea
// XioniaPollMessages() y decide si notificar. Por eso sobrevive a que
// cierres la app de recientes: aunque la Activity y su FlutterEngine
// mueran, este isolate sigue vivo dentro del foreground service.
// ============================================================================
Future<void> initializeBackgroundService() async {
  final service = FlutterBackgroundService();

  const serviceChannel = AndroidNotificationChannel(
    _serviceChannelId,
    'XionIA Mesh',
    description: 'Nodo mesh soberano activo',
    importance: Importance.low,
  );
  const msgChannel = AndroidNotificationChannel(
    _msgChannelId,
    'Mensajes XionChat',
    description: 'Mensajes entrantes cifrados E2E',
    importance: Importance.high,
  );

  final notifPlugin = FlutterLocalNotificationsPlugin();
  final androidImpl = notifPlugin
      .resolvePlatformSpecificImplementation<AndroidFlutterLocalNotificationsPlugin>();
  await androidImpl?.createNotificationChannel(serviceChannel);
  await androidImpl?.createNotificationChannel(msgChannel);

  await service.configure(
    androidConfiguration: AndroidConfiguration(
      onStart: onServiceStart,
      autoStart: true,
      autoStartOnBoot: false,
      isForegroundMode: true,
      notificationChannelId: _serviceChannelId,
      initialNotificationTitle: 'XionChat',
      initialNotificationContent: 'Nodo mesh conectado',
      foregroundServiceNotificationId: 888,
    ),
    iosConfiguration: IosConfiguration(),
  );
  service.startService();
}

@pragma('vm:entry-point')
void onServiceStart(ServiceInstance service) async {
  // Necesario para que plugins con canal de plataforma (path_provider,
  // flutter_local_notifications) funcionen en este isolate aparte.
  DartPluginRegistrant.ensureInitialized();
  // ignore: avoid_print
  print('[XION-SVC] onServiceStart: isolate iniciado');

  Xionia.init();
  // ignore: avoid_print
  print('[XION-SVC] Xionia.ready=${Xionia.ready}');
  if (!Xionia.ready) return; // sin .so no hay nada que hacer acá

  final dir = await getApplicationDocumentsDirectory();
  Xionia.setDataDir(dir.path);

  // Reconectar solo si ya había un faro configurado de una sesión
  // anterior — no forzar conexión la primerísima vez que se instala.
  final savedStatus = Xionia.getFaroAddr();
  final savedAddr = savedStatus.split(' (').first.trim();
  if (savedAddr.isNotEmpty && savedAddr != 'off') {
    // ignore: avoid_print
    print('[XION-SVC] connectFaro($savedAddr)');
    Xionia.connectFaro(savedAddr);
  }

  final notifPlugin = FlutterLocalNotificationsPlugin();
  await notifPlugin.initialize(
    const InitializationSettings(
      android: AndroidInitializationSettings('@mipmap/ic_launcher'),
    ),
  );

  // Qué chat está mirando el usuario ahora mismo (si hay alguno abierto).
  // La UI nos lo informa por invoke() cada vez que entra/sale de un chat.
  String? activeDid;
  String? activeAlias;
  service.on('setActiveChat').listen((event) {
    activeDid = event?['did'] as String?;
    activeAlias = event?['alias'] as String?;
  });
  service.on('connectFaro').listen((event) {
    final addr = event?['addr'] as String?;
    if (addr != null && addr.isNotEmpty) {
      // ignore: avoid_print
      print('[XION-SVC] connectFaro(invoke)=$addr');
      Xionia.connectFaro(addr);
    }
  });

  Timer.periodic(const Duration(seconds: 5), (timer) {
    if (!Xionia.ready) return;
    List<dynamic> polled;
    try {
      polled = Xionia.pollMessages();
    } catch (_) {
      return;
    }
    if (polled.isNotEmpty) {
      // ignore: avoid_print
      print('[XION-SVC] poll: ${polled.length} mensaje(s)');
    }
    for (final raw in polled) {
      final m = raw.toString();
      // Reenviar a la UI si está escuchando (chat abierto en pantalla).
      service.invoke('message', {'text': m});

      // Si el mensaje es justo del chat que el usuario tiene abierto en
      // este momento, no hace falta notificar — ya lo está viendo.
      final isActiveChat = activeDid != null &&
          (m.contains(activeDid!) || (activeAlias != null && m.contains(activeAlias!)));
      if (isActiveChat) continue;

      final idx = m.indexOf(': ');
      final sender = idx == -1 ? m : m.substring(0, idx);
      final text = idx == -1 ? '' : m.substring(idx + 2).replaceFirst('CHAT:', '');

      const androidDetails = AndroidNotificationDetails(
        _msgChannelId,
        'Mensajes XionChat',
        channelDescription: 'Mensajes entrantes cifrados E2E',
        importance: Importance.high,
        priority: Priority.high,
      );
      notifPlugin.show(
        DateTime.now().millisecondsSinceEpoch ~/ 1000,
        '💬 $sender',
        text,
        const NotificationDetails(android: androidDetails),
      );
    }
  });
}

// ============================================================================
// MESSAGE BUS (lado UI)
// Ya no sondea directo — recibe los mensajes que el background service
// le reenvía por invoke('message', ...). Mantiene el mismo buffer en
// RAM y la misma API que ya usaban HomeScreen/ChatScreen.
// ============================================================================
class MessageBus {
  MessageBus._();
  static final MessageBus instance = MessageBus._();

  final _controller = StreamController<String>.broadcast();
  Stream<String> get stream => _controller.stream;
  StreamSubscription? _serviceSub;

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
        .where((e) => e.mine ? e.did == did : (e.display.contains(alias) || e.display.contains(did)))
        .map((e) => e.display)
        .toList();
  }

  /// Empieza a escuchar los mensajes que reenvía el background service.
  void listenToService() {
    if (_serviceSub != null) return;
    _serviceSub = FlutterBackgroundService().on('message').listen((event) {
      final s = (event?['text'] ?? '').toString();
      if (s.isEmpty) return;
      _remember(_BusEntry(mine: false, did: null, display: s));
      _controller.add(s);
    });
  }

  // Qué chat está mirando el usuario ahora mismo, del lado de la UI
  // (para que ChatScreen sepa filtrar qué mostrar). Se mantiene en
  // paralelo al que se le informa al background service.
  String? activeChatDid;
  String? activeChatAlias;
}

class _BusEntry {
  final bool mine;
  final String? did;
  final String display;
  _BusEntry({required this.mine, required this.did, required this.display});
}

void main() async {
  WidgetsFlutterBinding.ensureInitialized();
  if (Platform.isAndroid) {
    await initializeBackgroundService();
  }
  runApp(const XionChatApp());
}

class XionChatApp extends StatelessWidget {
  const XionChatApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'XionChat',
      debugShowCheckedModeBanner: false,
      theme: ThemeData.dark().copyWith(
        colorScheme: ColorScheme.dark(
          primary: const Color(0xFF128C7E),
          secondary: const Color(0xFF25D366),
          surface: const Color(0xFF121212),
        ),
        scaffoldBackgroundColor: const Color(0xFF0B141A),
      ),
      home: const HomeScreen(),
    );
  }
}

class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key});

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> with WidgetsBindingObserver {
  String myDID = '...';
  String faroStatus = 'off';
  List<dynamic> contacts = [];
  String? _loadError;
  String _lastFaroAddr = '';

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

    // Permisos: notificaciones (Android 13+) y exención de batería, para
    // que el foreground service no lo mate el optimizador del fabricante.
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

    setState(() {
      myDID = Xionia.getMyDID();
      faroStatus = Xionia.getFaroAddr();
      final raw = faroStatus.split(' (').first.trim();
      if (raw.isNotEmpty && raw != 'off') _lastFaroAddr = raw;
      _loadContacts();
    });

    MessageBus.instance.listenToService();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed && Xionia.ready) {
      // El background service ya se encarga de mantener la conexión y
      // el polling solo. Acá solo refrescamos lo que se muestra en
      // pantalla por si cambió mientras estábamos afuera.
      setState(() => faroStatus = Xionia.getFaroAddr());
    }
  }

  void _loadContacts() {
    setState(() => contacts = Xionia.getContacts());
  }

  void _connect() {
    showDialog(
      context: context,
      builder: (_) {
        final ctrl = TextEditingController(text: '190.220.45.26:54321');
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
                enabledBorder: UnderlineInputBorder(borderSide: BorderSide(color: Color(0xFF128C7E))),
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
                        final r = await Isolate.run(() => Xionia.connectFaro(addr)); // ← FIX (era connectFaroAsync)
                        _lastFaroAddr = addr;
                        // avisamos también al background service, por si
                        // el usuario cambió de faro a mano
                        FlutterBackgroundService().invoke('connectFaro', {'addr': addr});
                        if (context.mounted) Navigator.pop(context);
                        if (mounted) {
                          ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(r), backgroundColor: const Color(0xFF1F2C34)));
                          setState(() => faroStatus = Xionia.getFaroAddr());
                        }
                      },
                child: connecting
                    ? const SizedBox(
                        width: 16,
                        height: 16,
                        child: CircularProgressIndicator(strokeWidth: 2, color: Color(0xFF25D366)),
                      )
                    : const Text('Conectar', style: TextStyle(color: Color(0xFF25D366))),
              ),
            ],
          ),
        );
      },
    );
  }

  void _showMyIdentity() {
    final id = Xionia.getMyIdentity();
    Clipboard.setData(ClipboardData(text: id));
    showDialog(
      context: context,
      builder: (_) => AlertDialog(
        backgroundColor: const Color(0xFF1F2C34),
        title: const Text('Mi Identidad', style: TextStyle(color: Colors.white)),
        content: SingleChildScrollView(child: SelectableText(id, style: const TextStyle(color: Colors.white, fontFamily: 'monospace', fontSize: 12))),
        actions: [
          TextButton(
            onPressed: () {
              Clipboard.setData(ClipboardData(text: id));
              Navigator.pop(context);
              ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Copiado'), duration: Duration(seconds: 1)));
            },
            child: const Text('Copiar', style: TextStyle(color: Color(0xFF25D366))),
          ),
          TextButton(onPressed: () => Navigator.pop(context), child: const Text('Cerrar', style: TextStyle(color: Colors.grey))),
        ],
      ),
    );
  }

  void _shareACL() {
    final packet = Xionia.exportACL();
    Clipboard.setData(ClipboardData(text: packet));
    showDialog(
      context: context,
      builder: (_) => AlertDialog(
        backgroundColor: const Color(0xFF1F2C34),
        title: const Text('Compartir Red', style: TextStyle(color: Colors.white)),
        content: SingleChildScrollView(
          child: SelectableText(packet, style: const TextStyle(color: Colors.white, fontFamily: 'monospace', fontSize: 13)),
        ),
        actions: [
          TextButton(
            onPressed: () {
              Clipboard.setData(ClipboardData(text: packet));
              Navigator.pop(context);
              ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Copiado al portapapeles'), duration: Duration(seconds: 1)));
            },
            child: const Text('Copiar', style: TextStyle(color: Color(0xFF25D366))),
          ),
          TextButton(onPressed: () => Navigator.pop(context), child: const Text('Cerrar', style: TextStyle(color: Colors.grey))),
        ],
      ),
    );
  }

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
                border: OutlineInputBorder(borderSide: BorderSide(color: Color(0xFF128C7E))),
              ),
            ),
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(context), child: const Text('Cancelar', style: TextStyle(color: Colors.grey))),
            TextButton(
              onPressed: () {
                Xionia.importACL(ctrl.text);
                Xionia.reloadACL();
                _loadContacts();
                Navigator.pop(context);
                ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Contacto agregado'), duration: Duration(seconds: 1)));
              },
              child: const Text('Agregar', style: TextStyle(color: Color(0xFF25D366))),
            ),
          ],
        );
      },
    );
  }

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
            enabledBorder: UnderlineInputBorder(borderSide: BorderSide(color: Color(0xFF128C7E))),
          ),
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context), child: const Text('Cancelar', style: TextStyle(color: Colors.grey))),
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

  void _openChat(String did, String displayName) {
    Navigator.push(
      context,
      MaterialPageRoute(builder: (_) => ChatScreen(did: did, alias: displayName)),
    );
  }

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
              title: const Text('RESET TOTAL (borra todo)', style: TextStyle(color: Colors.red)),
              onTap: () {
                Xionia.reset();
                setState(() {
                  myDID = Xionia.getMyDID();
                  contacts = [];
                  faroStatus = 'off';
                });
                Navigator.pop(context);
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

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

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
            icon: Icon(faroStatus.contains('off') ? Icons.cloud_off : Icons.cloud_done, color: const Color(0xFF25D366)),
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
              const PopupMenuItem(value: 'identity', child: Text('Mi Identidad', style: TextStyle(color: Colors.white))),
              const PopupMenuItem(value: 'share', child: Text('Compartir Red', style: TextStyle(color: Colors.white))),
              const PopupMenuItem(value: 'reset', child: Text('RESET', style: TextStyle(color: Colors.red))),
            ],
          ),
        ],
      ),
      body: contacts.isEmpty
          ? const Center(child: Text('Sin contactos. Agregá uno con el +', style: TextStyle(color: Colors.grey)))
          : ListView.builder(
              itemCount: contacts.length,
              itemBuilder: (_, i) {
                final c = contacts[i];
                final did = c['did'].toString();
                final alias = (c['alias'] ?? '').toString();
                final display = alias.isNotEmpty ? alias : did;
                return ListTile(
                  leading: CircleAvatar(
                    backgroundColor: const Color(0xFF128C7E),
                    child: Text(display[0].toUpperCase(), style: const TextStyle(color: Colors.white)),
                  ),
                  title: Text(display, style: const TextStyle(color: Colors.white)),
                  subtitle: Text(did, style: const TextStyle(color: Colors.grey, fontSize: 12), maxLines: 1, overflow: TextOverflow.ellipsis),
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

class ChatScreen extends StatefulWidget {
  final String did;
  final String alias;
  const ChatScreen({super.key, required this.did, required this.alias});

  @override
  State<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends State<ChatScreen> {
  final ctrl = TextEditingController();
  final List<String> msgs = [];
  StreamSubscription<String>? _sub;
  final ScrollController _scrollCtrl = ScrollController();
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
    MessageBus.instance.activeChatDid = widget.did;
    MessageBus.instance.activeChatAlias = widget.alias;
    // avisamos al background service para que no notifique este chat
    // mientras lo tenés abierto
    FlutterBackgroundService().invoke('setActiveChat', {'did': widget.did, 'alias': widget.alias});

    _loadHistory();
    _sub = MessageBus.instance.stream.listen((m) {
      if (m.contains(widget.alias) || m.contains(widget.did)) {
        setState(() => msgs.add(m));
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
        fromFile = data.map((e) => e.toString()).toList();
        _recording = true;
      }
    } catch (_) {}

    final fromRam = MessageBus.instance.recentFor(widget.did, widget.alias);
    final merged = [...fromFile];
    for (final m in fromRam) {
      if (!merged.contains(m)) merged.add(m);
    }
    if (merged.isNotEmpty) {
      setState(() => msgs.addAll(merged));
      _scrollToEnd();
    }
  }

  Future<void> _saveIfRecording() async {
    if (!_recording) return;
    try {
      final f = await _historyFile();
      await f.writeAsString(jsonEncode(msgs));
    } catch (_) {}
  }

  Future<void> _toggleRecording() async {
    setState(() => _recording = !_recording);
    if (_recording) {
      await _saveIfRecording();
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Grabando conversación'), duration: Duration(seconds: 1)),
        );
      }
    } else {
      try {
        final f = await _historyFile();
        if (await f.exists()) await f.delete();
      } catch (_) {}
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Chat efímero: se borra al salir'), duration: Duration(seconds: 1)),
        );
      }
    }
  }

  void _scrollToEnd() {
    Future.delayed(const Duration(milliseconds: 100), () {
      if (_scrollCtrl.hasClients) {
        _scrollCtrl.animateTo(_scrollCtrl.position.maxScrollExtent, duration: const Duration(milliseconds: 200), curve: Curves.easeOut);
      }
    });
  }

  void _send() {
    if (ctrl.text.isEmpty) return;
    final r = Xionia.sendChat(widget.did, ctrl.text);
    final display = 'Tú: ${ctrl.text}';
    setState(() => msgs.add(display));
    MessageBus.instance.addSent(widget.did, display);
    _saveIfRecording();
    ctrl.clear();
    _scrollToEnd();
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(r), duration: const Duration(seconds: 1), backgroundColor: const Color(0xFF1F2C34)));
  }

  @override
  void dispose() {
    if (MessageBus.instance.activeChatDid == widget.did) {
      MessageBus.instance.activeChatDid = null;
      MessageBus.instance.activeChatAlias = null;
      FlutterBackgroundService().invoke('setActiveChat', {'did': null, 'alias': null});
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
            tooltip: _recording ? 'Grabando (tocá para volver a efímero)' : 'Chat efímero (tocá para grabar)',
            icon: Icon(_recording ? Icons.fiber_manual_record : Icons.radio_button_unchecked,
                color: _recording ? Colors.redAccent : Colors.grey),
            onPressed: _toggleRecording,
          ),
        ],
      ),
      body: Column(
        children: [
          Expanded(
            child: ListView.builder(
              controller: _scrollCtrl,
              itemCount: msgs.length,
              itemBuilder: (_, i) {
                final isMe = msgs[i].startsWith('Tú:');
                // ← FIX: limpieza del prefijo CHAT: del motor
                final text = msgs[i]
                    .replaceFirst('Tú: ', '')
                    .replaceFirst(RegExp(r'^[^:]+: '), '')
                    .replaceFirst(RegExp(r'^CHAT:\s*'), '');
                return Align(
                  alignment: isMe ? Alignment.centerRight : Alignment.centerLeft,
                  child: Container(
                    margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
                    padding: const EdgeInsets.all(12),
                    constraints: BoxConstraints(maxWidth: MediaQuery.of(context).size.width * 0.75),
                    decoration: BoxDecoration(
                      color: isMe ? const Color(0xFF005C4B) : const Color(0xFF1F2C34),
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
                    controller: ctrl,
                    style: const TextStyle(color: Colors.white),
                    decoration: const InputDecoration(hintText: 'Mensaje...', hintStyle: TextStyle(color: Colors.grey), border: InputBorder.none),
                    onSubmitted: (_) => _send(),
                  ),
                ),
                IconButton(icon: const Icon(Icons.send, color: Color(0xFF25D366)), onPressed: _send),
              ],
            ),
          ),
        ],
      ),
    );
  }
}
