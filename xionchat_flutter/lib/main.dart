import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:path_provider/path_provider.dart';
import 'package:permission_handler/permission_handler.dart';
import 'flutter_bridge.dart';

/// Único punto que llama a Xionia.pollMessages(). XioniaPollMessages()
/// del lado Go VACÍA la cola en cada llamada — si dos widgets sondean
/// por su cuenta (como pasaba antes con HomeScreen y ChatScreen a la
/// vez), el que gana la carrera se queda con el mensaje y el otro no
/// lo ve nunca. Acá hay un solo timer que reparte cada mensaje a quien
/// esté escuchando el stream.
class MessageBus {
  MessageBus._();
  static final MessageBus instance = MessageBus._();

  final _controller = StreamController<String>.broadcast();
  Stream<String> get stream => _controller.stream;
  Timer? _timer;

  // Buffer circular en RAM (no se guarda a disco). Vive mientras el
  // proceso de la app esté vivo; si la matás del todo, se pierde —
  // sigue siendo "efímero" en ese sentido, pero sobrevive a cerrar y
  // reabrir la pantalla de chat dentro de la misma sesión.
  final List<_BusEntry> _recent = [];
  static const _recentCap = 500;

  void _remember(_BusEntry e) {
    _recent.add(e);
    if (_recent.length > _recentCap) _recent.removeAt(0);
  }

  /// Mensajes propios enviados desde este dispositivo (no se
  /// retransmiten por el stream, solo quedan para poder reconstruir
  /// el chat si lo cerrás y volvés a abrir).
  void addSent(String did, String displayText) {
    _remember(_BusEntry(mine: true, did: did, display: displayText));
  }

  /// Historial en RAM para un contacto puntual: lo que llegó mientras
  /// el chat estaba cerrado + lo que mandaste vos.
  List<String> recentFor(String did, String alias) {
    return _recent
        .where((e) => e.mine ? e.did == did : (e.display.contains(alias) || e.display.contains(did)))
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

  /// Sondea ahora mismo, sin esperar al próximo tick del timer (útil al
  /// volver del background, para no perder mensajes que llegaron
  /// mientras la app estaba pausada).
  void pollNow() => _tick();

  void _tick() {
    if (!Xionia.ready) return;
    try {
      final polled = Xionia.pollMessages();
      for (final m in polled) {
        final s = m.toString();
        _remember(_BusEntry(mine: false, did: null, display: s));
        _controller.add(s);
      }
    } catch (_) {
      // sin conexión / sin identidad todavía: ignorar este ciclo
    }
  }
}

class _BusEntry {
  final bool mine;
  final String? did; // solo para mensajes propios: a quién se lo mandaste
  final String display; // texto ya formateado, listo para mostrar
  _BusEntry({required this.mine, required this.did, required this.display});
}

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

  // Foreground service + notificaciones: mantienen la conexión viva en
  // background y avisan de mensajes nuevos aunque la app esté cerrada
  // (no matada del todo, solo en segundo plano).
  static const _serviceChannel = MethodChannel('com.xionia/service');
  final _notif = FlutterLocalNotificationsPlugin();
  StreamSubscription<String>? _notifSub;
  AppLifecycleState _appState = AppLifecycleState.resumed;

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

    // Foreground service + permisos: para que Android no mate la red
    // en background. Si algo de esto falla (permiso denegado, etc.)
    // no bloquea el resto de la app — sigue andando en foreground.
    if (Platform.isAndroid) {
      try {
        await _serviceChannel.invokeMethod('startService');
      } catch (_) {}
      try {
        if (await Permission.notification.isDenied) {
          await Permission.notification.request();
        }
        if (await Permission.ignoreBatteryOptimizations.isDenied) {
          await Permission.ignoreBatteryOptimizations.request();
        }
      } catch (_) {}
    }
    await _initNotifications();
    _notifSub = MessageBus.instance.stream.listen(_onIncomingForNotification);

    final dir = await getApplicationDocumentsDirectory();
    Xionia.setDataDir(dir.path);
    setState(() {
      myDID = Xionia.getMyDID();
      faroStatus = Xionia.getFaroAddr();
      // faroStatus viene como "IP:PUERTO (UDP)" / "(WS)" / "(off)" —
      // nos quedamos solo con la dirección para poder reconectar solos.
      final raw = faroStatus.split(' (').first;
      if (raw.isNotEmpty && raw != 'off') _lastFaroAddr = raw;
      _loadContacts();
    });
    MessageBus.instance.start();
  }

  Future<void> _initNotifications() async {
    const androidSettings = AndroidInitializationSettings('@mipmap/ic_launcher');
    try {
      await _notif.initialize(const InitializationSettings(android: androidSettings));
    } catch (_) {
      // sin notificaciones no es fatal, la app sigue funcionando en foreground
    }
  }

  void _onIncomingForNotification(String m) {
    // Solo mientras la app no está en primer plano — si el usuario ya
    // está mirando el chat, no hace falta molestarlo con una notif.
    if (_appState == AppLifecycleState.resumed) return;
    // m viene como "alias: CHAT:texto" — separamos remitente y texto.
    final idx = m.indexOf(': ');
    final sender = idx == -1 ? m : m.substring(0, idx);
    final text = idx == -1 ? '' : m.substring(idx + 2).replaceFirst('CHAT:', '');
    const androidDetails = AndroidNotificationDetails(
      'xionia_messages',
      'Mensajes XionChat',
      channelDescription: 'Mensajes entrantes cifrados E2E',
      importance: Importance.high,
      priority: Priority.high,
    );
    _notif.show(
      DateTime.now().millisecondsSinceEpoch ~/ 1000,
      '💬 $sender',
      text,
      const NotificationDetails(android: androidDetails),
    );
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    _appState = state;
    if (state == AppLifecycleState.resumed && Xionia.ready && _lastFaroAddr.isNotEmpty) {
      // La app volvió de segundo plano: Android puede haber matado el
      // mapeo NAT del socket UDP mientras estaba en Doze/suspendida.
      // Reconectamos y forzamos un poll ya mismo en vez de esperar
      // pasivamente al próximo tick del ticker de 15s del lado Go.
      // Usamos la versión async para no congelar la UI en el instante
      // en que el usuario recién está reabriendo la app.
      Future(() => Xionia.connectFaro(_lastFaroAddr)).then((_) {
        MessageBus.instance.pollNow();
        if (mounted) setState(() => faroStatus = Xionia.getFaroAddr());
      });
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
                        // connectFaroAsync corre en otro isolate: no
                        // congela la UI aunque el faro tarde en
                        // responder (o esté caído).
                        final r = Xionia.connectFaro(addr);
                        _lastFaroAddr = addr;
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
                Xionia.reloadACL(); // 🔥 Recargar ACL en el nodo
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
              Xionia.reloadACL(); // 🔥 Por si acaso
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
    _notifSub?.cancel();
    // El foreground service NO se detiene acá a propósito: la idea es
    // que siga vivo mientras la app esté en background, no solo
    // mientras HomeScreen esté montado.
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

  // Grabación: por defecto el chat es efímero (se borra al salir).
  // Si el usuario prende el ON, se guarda en un archivo local por
  // contacto y se recupera la próxima vez que entre a ese chat.
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
    _loadHistory();
    _sub = MessageBus.instance.stream.listen((m) {
      // m viene como "alias: CHAT:eyyy" o "did:maia:...: CHAT:eyyy"
      if (m.contains(widget.alias) || m.contains(widget.did)) {
        setState(() => msgs.add(m));
        _saveIfRecording();
        _scrollToEnd();
      }
    });
  }

  Future<void> _loadHistory() async {
    // 1) si hay historial guardado en disco (grabación estaba ON), lo cargamos primero.
    var fromFile = <String>[];
    try {
      final f = await _historyFile();
      if (await f.exists()) {
        final data = jsonDecode(await f.readAsString()) as List<dynamic>;
        fromFile = data.map((e) => e.toString()).toList();
        _recording = true; // si hay historial guardado, asumimos que seguía ON
      }
    } catch (_) {
      // sin historial o archivo corrupto: seguimos con lo que haya en RAM
    }

    // 2) sumamos lo que haya en el buffer en RAM (mensajes que llegaron
    // o mandaste mientras el chat estaba cerrado en esta misma sesión
    // de la app) que no esté ya en el archivo, para no duplicar.
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
    } catch (_) {
      // si falla el guardado no interrumpimos el chat
    }
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
                final text = msgs[i].replaceFirst('Tú: ', '').replaceFirst(RegExp(r'^[^:]+: '), '');
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
