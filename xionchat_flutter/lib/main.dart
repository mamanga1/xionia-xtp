import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:path_provider/path_provider.dart';
import 'flutter_bridge.dart';

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

class _HomeScreenState extends State<HomeScreen> {
  String myDID = '...';
  String faroStatus = 'off';
  List<dynamic> contacts = [];
  List<dynamic> messages = [];
  Timer? _pollTimer;

  @override
  void initState() {
    super.initState();
    _init();
  }

  Future<void> _init() async {
    final dir = await getApplicationDocumentsDirectory();
    Xionia.setDataDir(dir.path);
    setState(() {
      myDID = Xionia.getMyDID();
      faroStatus = Xionia.getFaroAddr();
      _loadContacts();
    });
    _pollTimer = Timer.periodic(const Duration(seconds: 3), (_) => _poll());
  }

  void _loadContacts() {
    setState(() => contacts = Xionia.getContacts());
  }

  void _poll() {
    final msgs = Xionia.pollMessages();
    if (msgs.isNotEmpty) {
      setState(() => messages.addAll(msgs));
    }
  }

  void _connect() {
    showDialog(
      context: context,
      builder: (_) {
        final ctrl = TextEditingController(text: '190.220.45.26:54321');
        return AlertDialog(
          backgroundColor: const Color(0xFF1F2C34),
          title: const Text('Conectar al Faro', style: TextStyle(color: Colors.white)),
          content: TextField(
            controller: ctrl,
            style: const TextStyle(color: Colors.white),
            decoration: const InputDecoration(
              labelText: 'IP:PUERTO',
              labelStyle: TextStyle(color: Colors.grey),
              enabledBorder: UnderlineInputBorder(borderSide: BorderSide(color: Color(0xFF128C7E))),
            ),
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(context), child: const Text('Cancelar', style: TextStyle(color: Colors.grey))),
            TextButton(
              onPressed: () {
                final r = Xionia.connectFaro(ctrl.text);
                Navigator.pop(context);
                ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(r), backgroundColor: const Color(0xFF1F2C34)));
                setState(() => faroStatus = Xionia.getFaroAddr());
              },
              child: const Text('Conectar', style: TextStyle(color: Color(0xFF25D366))),
            ),
          ],
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
    _pollTimer?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
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
  Timer? _timer;
  final ScrollController _scrollCtrl = ScrollController();

  @override
  void initState() {
    super.initState();
    _timer = Timer.periodic(const Duration(seconds: 2), (_) {
      final polled = Xionia.pollMessages();
      for (final m in polled) {
        // m viene como "alias: CHAT:eyyy" o "did:maia:...: CHAT:eyyy"
        if (m.toString().contains(widget.alias) || m.toString().contains(widget.did)) {
          setState(() => msgs.add(m.toString()));
          Future.delayed(const Duration(milliseconds: 100), () {
            if (_scrollCtrl.hasClients) {
              _scrollCtrl.animateTo(_scrollCtrl.position.maxScrollExtent, duration: const Duration(milliseconds: 200), curve: Curves.easeOut);
            }
          });
        }
      }
    });
  }

  void _send() {
    if (ctrl.text.isEmpty) return;
    final r = Xionia.sendChat(widget.did, ctrl.text);
    setState(() => msgs.add('Tú: ${ctrl.text}'));
    ctrl.clear();
    Future.delayed(const Duration(milliseconds: 100), () {
      if (_scrollCtrl.hasClients) {
        _scrollCtrl.animateTo(_scrollCtrl.position.maxScrollExtent, duration: const Duration(milliseconds: 200), curve: Curves.easeOut);
      }
    });
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(r), duration: const Duration(seconds: 1), backgroundColor: const Color(0xFF1F2C34)));
  }

  @override
  void dispose() {
    _timer?.cancel();
    _scrollCtrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: const Color(0xFF0B141A),
      appBar: AppBar(backgroundColor: const Color(0xFF1F2C34), title: Text(widget.alias, style: const TextStyle(color: Colors.white))),
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
