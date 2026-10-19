//! Shell copy for the 13 languages agenthub ships.
//!
//! The rule here is the same one `web/tests/copy-fallback.spec.js` enforces on
//! the console: English is complete and is the fallback, and every other
//! language carries its own translation for every key. A key that merely
//! repeats the English string is a key nobody translated, so the completeness
//! test rejects it.
//!
//! The shell owns very little prose — the tray menu, the connection page and a
//! couple of error reasons. Everything else is the console's own copy, which
//! the desktop app loads unchanged.

/// `(code, native name)` — kept aligned with `web/src/lib/languages.js`.
pub const LANGUAGES: [(&str, &str); 13] = [
    ("en", "English"),
    ("zh", "简体中文"),
    ("zh-TW", "繁體中文"),
    ("es", "Español"),
    ("fr", "Français"),
    ("de", "Deutsch"),
    ("it", "Italiano"),
    ("ru", "Русский"),
    ("ja", "日本語"),
    ("ko", "한국어"),
    ("pt", "Português"),
    ("ar", "العربية"),
    ("hi", "हिन्दी"),
];

pub const EN: &[(&str, &str)] = &[
    ("trayOpen", "Open console"),
    ("traySettings", "Connection settings…"),
    ("trayAbout", "About"),
    ("trayQuit", "Quit"),
    ("connectTitle", "Connect to agenthub"),
    ("fieldServer", "Server address"),
    ("fieldInsecure", "Ignore self-signed certificates"),
    (
        "fieldInsecureNote",
        "Only for servers on your own network: the app stops checking who it is talking to.",
    ),
    ("fieldLanguage", "Language"),
    ("actionConnect", "Connect"),
    ("actionConnecting", "Connecting…"),
    ("actionLater", "Later"),
    ("actionRetry", "Retry"),
    ("statusChecking", "Checking…"),
    ("statusConnected", "Connected"),
    ("errEmpty", "Enter the server address first."),
    ("errInvalid", "That is not an address: it must start with http:// or https://."),
    ("errUnreachable", "Cannot reach that server."),
    (
        "errCertificate",
        "The certificate is not trusted — ignore self-signed certificates to connect anyway.",
    ),
    ("errUnexpected", "Something else went wrong while connecting."),
    ("aboutVersion", "Version"),
    ("aboutBuild", "Build"),
    ("aboutServer", "Server"),
    ("consoleMissing", "The console bundle is missing: build the web app first."),
];

pub const ZH: &[(&str, &str)] = &[
    ("trayOpen", "打开控制台"),
    ("traySettings", "连接设置…"),
    ("trayAbout", "关于"),
    ("trayQuit", "退出"),
    ("connectTitle", "连接到 agenthub"),

    ("fieldServer", "服务端地址"),
    ("fieldInsecure", "忽略自签证书"),
    ("fieldInsecureNote", "仅用于你自己网络中的服务端：开启后应用不再校验对方身份。"),
    ("fieldLanguage", "语言"),
    ("actionConnect", "连接"),
    ("actionConnecting", "正在连接…"),
    ("actionLater", "以后再说"),
    ("actionRetry", "重试"),
    ("statusChecking", "正在检查…"),
    ("statusConnected", "已连接"),
    ("errEmpty", "请先填写服务端地址。"),
    ("errInvalid", "这不是一个合法地址：必须以 http:// 或 https:// 开头。"),
    ("errUnreachable", "无法访问该服务端。"),
    ("errCertificate", "证书不受信；可勾选“忽略自签证书”后继续连接。"),
    ("errUnexpected", "连接过程中出现了其他错误。"),
    ("aboutVersion", "版本"),
    ("aboutBuild", "构建"),
    ("aboutServer", "服务端"),
    ("consoleMissing", "缺少控制台产物：请先构建前端。"),
];

pub const ZH_TW: &[(&str, &str)] = &[
    ("trayOpen", "開啟主控台"),
    ("traySettings", "連線設定…"),
    ("trayAbout", "關於"),
    ("trayQuit", "結束"),
    ("connectTitle", "連線到 agenthub"),

    ("fieldServer", "伺服器位址"),
    ("fieldInsecure", "忽略自簽憑證"),
    ("fieldInsecureNote", "僅適用於你自己網路中的伺服器：開啟後應用不再驗證對方身分。"),
    ("fieldLanguage", "語言"),
    ("actionConnect", "連線"),
    ("actionConnecting", "正在連線…"),
    ("actionLater", "稍後再說"),
    ("actionRetry", "重試"),
    ("statusChecking", "正在檢查…"),
    ("statusConnected", "已連線"),
    ("errEmpty", "請先填寫伺服器位址。"),
    ("errInvalid", "這不是合法位址：必須以 http:// 或 https:// 開頭。"),
    ("errUnreachable", "無法連上該伺服器。"),
    ("errCertificate", "憑證不受信任；勾選「忽略自簽憑證」即可繼續連線。"),
    ("errUnexpected", "連線時發生其他錯誤。"),
    ("aboutVersion", "版本"),
    ("aboutBuild", "建置"),
    ("aboutServer", "伺服器"),
    ("consoleMissing", "缺少主控台產物：請先建置前端。"),
];

pub const ES: &[(&str, &str)] = &[
    ("trayOpen", "Abrir la consola"),
    ("traySettings", "Ajustes de conexión…"),
    ("trayAbout", "Acerca de"),
    ("trayQuit", "Salir"),
    ("connectTitle", "Conectarse a agenthub"),

    ("fieldServer", "Dirección del servidor"),
    ("fieldInsecure", "Ignorar certificados autofirmados"),
    (
        "fieldInsecureNote",
        "Solo para servidores de tu propia red: la aplicación deja de comprobar con quién habla.",
    ),
    ("fieldLanguage", "Idioma"),
    ("actionConnect", "Conectar"),
    ("actionConnecting", "Conectando…"),
    ("actionLater", "Más tarde"),
    ("actionRetry", "Reintentar"),
    ("statusChecking", "Comprobando…"),
    ("statusConnected", "Conectado"),
    ("errEmpty", "Escribe primero la dirección del servidor."),
    ("errInvalid", "Eso no es una dirección: debe empezar por http:// o https://."),
    ("errUnreachable", "No se puede alcanzar ese servidor."),
    (
        "errCertificate",
        "El certificado no es de confianza: marca «ignorar certificados autofirmados» para seguir.",
    ),
    ("errUnexpected", "Algo más falló al conectar."),
    ("aboutVersion", "Versión"),
    ("aboutBuild", "Compilación"),
    ("aboutServer", "Servidor"),
    ("consoleMissing", "Falta el paquete de la consola: compila antes la aplicación web."),
];

pub const FR: &[(&str, &str)] = &[
    ("trayOpen", "Ouvrir la console"),
    ("traySettings", "Paramètres de connexion…"),
    ("trayAbout", "À propos"),
    ("trayQuit", "Quitter"),
    ("connectTitle", "Se connecter à agenthub"),

    ("fieldServer", "Adresse du serveur"),
    ("fieldInsecure", "Ignorer les certificats auto-signés"),
    (
        "fieldInsecureNote",
        "Uniquement pour vos propres serveurs : l'application cesse de vérifier à qui elle parle.",
    ),
    ("fieldLanguage", "Langue"),
    ("actionConnect", "Se connecter"),
    ("actionConnecting", "Connexion…"),
    ("actionLater", "Plus tard"),
    ("actionRetry", "Réessayer"),
    ("statusChecking", "Vérification…"),
    ("statusConnected", "Connecté"),
    ("errEmpty", "Saisissez d'abord l'adresse du serveur."),
    ("errInvalid", "Ce n'est pas une adresse : elle doit commencer par http:// ou https://."),
    ("errUnreachable", "Impossible de joindre ce serveur."),
    (
        "errCertificate",
        "Le certificat n'est pas fiable ; cochez « ignorer les certificats auto-signés » pour continuer.",
    ),
    ("errUnexpected", "Une autre erreur est survenue pendant la connexion."),
    ("aboutVersion", "Numéro de version"),
    ("aboutBuild", "Compilation"),
    ("aboutServer", "Serveur"),
    ("consoleMissing", "Le bundle de la console est absent : compilez d'abord l'application web."),
];

pub const DE: &[(&str, &str)] = &[
    ("trayOpen", "Konsole öffnen"),
    ("traySettings", "Verbindungseinstellungen…"),
    ("trayAbout", "Über"),
    ("trayQuit", "Beenden"),
    ("connectTitle", "Mit agenthub verbinden"),
    ("fieldServer", "Serveradresse"),
    ("fieldInsecure", "Selbstsignierte Zertifikate ignorieren"),
    (
        "fieldInsecureNote",
        "Nur für Server im eigenen Netz: Die App prüft dann nicht mehr, mit wem sie spricht.",
    ),
    ("fieldLanguage", "Sprache"),
    ("actionConnect", "Verbinden"),
    ("actionConnecting", "Verbinden…"),
    ("actionLater", "Später"),
    ("actionRetry", "Erneut versuchen"),
    ("statusChecking", "Prüfen…"),
    ("statusConnected", "Verbunden"),
    ("errEmpty", "Gib zuerst die Serveradresse ein."),
    ("errInvalid", "Das ist keine Adresse: sie muss mit http:// oder https:// beginnen."),
    ("errUnreachable", "Dieser Server ist nicht erreichbar."),
    (
        "errCertificate",
        "Dem Zertifikat wird nicht vertraut: „Selbstsignierte Zertifikate ignorieren“ aktivieren, um trotzdem zu verbinden.",
    ),
    ("errUnexpected", "Beim Verbinden ist ein anderer Fehler aufgetreten."),
    ("aboutVersion", "Programmversion"),
    ("aboutBuild", "Erstellung"),
    ("aboutServer", "Zielserver"),
    ("consoleMissing", "Das Konsolen-Bundle fehlt: baue zuerst die Web-App."),
];

pub const IT: &[(&str, &str)] = &[
    ("trayOpen", "Apri la console"),
    ("traySettings", "Impostazioni di connessione…"),
    ("trayAbout", "Informazioni"),
    ("trayQuit", "Esci"),
    ("connectTitle", "Connettersi ad agenthub"),

    ("fieldServer", "Indirizzo del server"),
    ("fieldInsecure", "Ignora i certificati autofirmati"),
    (
        "fieldInsecureNote",
        "Solo per server nella tua rete: l'app smette di verificare con chi sta parlando.",
    ),
    ("fieldLanguage", "Lingua"),
    ("actionConnect", "Connetti"),
    ("actionConnecting", "Connessione…"),
    ("actionLater", "Più tardi"),
    ("actionRetry", "Riprova"),
    ("statusChecking", "Controllo…"),
    ("statusConnected", "Connesso"),
    ("errEmpty", "Inserisci prima l'indirizzo del server."),
    ("errInvalid", "Non è un indirizzo: deve iniziare con http:// o https://."),
    ("errUnreachable", "Impossibile raggiungere quel server."),
    (
        "errCertificate",
        "Il certificato non è attendibile: attiva «ignora i certificati autofirmati» per proseguire.",
    ),
    ("errUnexpected", "Si è verificato un altro errore durante la connessione."),
    ("aboutVersion", "Versione"),
    ("aboutBuild", "Compilazione"),
    ("aboutServer", "Destinazione"),
    ("consoleMissing", "Manca il bundle della console: compila prima l'app web."),
];

pub const RU: &[(&str, &str)] = &[
    ("trayOpen", "Открыть консоль"),
    ("traySettings", "Настройки подключения…"),
    ("trayAbout", "О программе"),
    ("trayQuit", "Выйти"),
    ("connectTitle", "Подключиться к agenthub"),

    ("fieldServer", "Адрес сервера"),
    ("fieldInsecure", "Игнорировать самоподписанные сертификаты"),
    (
        "fieldInsecureNote",
        "Только для серверов в вашей сети: приложение перестаёт проверять, с кем оно говорит.",
    ),
    ("fieldLanguage", "Язык"),
    ("actionConnect", "Подключиться"),
    ("actionConnecting", "Подключение…"),
    ("actionLater", "Позже"),
    ("actionRetry", "Повторить"),
    ("statusChecking", "Проверка…"),
    ("statusConnected", "Подключено"),
    ("errEmpty", "Сначала введите адрес сервера."),
    ("errInvalid", "Это не адрес: он должен начинаться с http:// или https://."),
    ("errUnreachable", "Не удаётся достучаться до этого сервера."),
    (
        "errCertificate",
        "Сертификат не является доверенным — отметьте «игнорировать самоподписанные сертификаты», чтобы продолжить.",
    ),
    ("errUnexpected", "При подключении произошла другая ошибка."),
    ("aboutVersion", "Версия"),
    ("aboutBuild", "Сборка"),
    ("aboutServer", "Сервер"),
    ("consoleMissing", "Сборка консоли отсутствует: сначала соберите веб-приложение."),
];

pub const JA: &[(&str, &str)] = &[
    ("trayOpen", "コンソールを開く"),
    ("traySettings", "接続設定…"),
    ("trayAbout", "このアプリについて"),
    ("trayQuit", "終了"),
    ("connectTitle", "agenthub に接続"),

    ("fieldServer", "サーバーアドレス"),
    ("fieldInsecure", "自己署名証明書を無視する"),
    (
        "fieldInsecureNote",
        "自分のネットワーク内のサーバー専用です。有効にするとアプリは相手の検証をやめます。",
    ),
    ("fieldLanguage", "言語"),
    ("actionConnect", "接続"),
    ("actionConnecting", "接続中…"),
    ("actionLater", "後で"),
    ("actionRetry", "再試行"),
    ("statusChecking", "確認中…"),
    ("statusConnected", "接続済み"),
    ("errEmpty", "まずサーバーアドレスを入力してください。"),
    ("errInvalid", "それはアドレスではありません。http:// または https:// で始める必要があります。"),
    ("errUnreachable", "そのサーバーに到達できません。"),
    (
        "errCertificate",
        "証明書を信頼できません。「自己署名証明書を無視する」を選ぶと接続できます。",
    ),
    ("errUnexpected", "接続中に別のエラーが発生しました。"),
    ("aboutVersion", "バージョン"),
    ("aboutBuild", "ビルド"),
    ("aboutServer", "サーバー"),
    ("consoleMissing", "コンソールのバンドルがありません。先にウェブアプリをビルドしてください。"),
];

pub const KO: &[(&str, &str)] = &[
    ("trayOpen", "콘솔 열기"),
    ("traySettings", "연결 설정…"),
    ("trayAbout", "정보"),
    ("trayQuit", "종료"),
    ("connectTitle", "agenthub에 연결"),

    ("fieldServer", "서버 주소"),
    ("fieldInsecure", "자체 서명 인증서 무시"),
    (
        "fieldInsecureNote",
        "직접 관리하는 네트워크의 서버에만 사용하세요. 앱이 상대를 확인하지 않습니다.",
    ),
    ("fieldLanguage", "언어"),
    ("actionConnect", "연결"),
    ("actionConnecting", "연결 중…"),
    ("actionLater", "나중에"),
    ("actionRetry", "다시 시도"),
    ("statusChecking", "확인 중…"),
    ("statusConnected", "연결됨"),
    ("errEmpty", "서버 주소를 먼저 입력하세요."),
    ("errInvalid", "주소가 아닙니다. http:// 또는 https:// 로 시작해야 합니다."),
    ("errUnreachable", "해당 서버에 연결할 수 없습니다."),
    (
        "errCertificate",
        "인증서를 신뢰할 수 없습니다. '자체 서명 인증서 무시'를 선택하면 연결할 수 있습니다.",
    ),
    ("errUnexpected", "연결 중 다른 오류가 발생했습니다."),
    ("aboutVersion", "버전"),
    ("aboutBuild", "빌드"),
    ("aboutServer", "서버"),
    ("consoleMissing", "콘솔 번들이 없습니다. 먼저 웹 앱을 빌드하세요."),
];

pub const PT: &[(&str, &str)] = &[
    ("trayOpen", "Abrir a consola"),
    ("traySettings", "Definições de ligação…"),
    ("trayAbout", "Sobre"),
    ("trayQuit", "Sair"),
    ("connectTitle", "Ligar ao agenthub"),

    ("fieldServer", "Endereço do servidor"),
    ("fieldInsecure", "Ignorar certificados autoassinados"),
    (
        "fieldInsecureNote",
        "Apenas para servidores da sua rede: a app deixa de verificar com quem fala.",
    ),
    ("fieldLanguage", "Idioma"),
    ("actionConnect", "Ligar"),
    ("actionConnecting", "A ligar…"),
    ("actionLater", "Mais tarde"),
    ("actionRetry", "Tentar novamente"),
    ("statusChecking", "A verificar…"),
    ("statusConnected", "Ligado"),
    ("errEmpty", "Introduza primeiro o endereço do servidor."),
    ("errInvalid", "Isso não é um endereço: tem de começar por http:// ou https://."),
    ("errUnreachable", "Não é possível alcançar esse servidor."),
    (
        "errCertificate",
        "O certificado não é fiável; assinale «ignorar certificados autoassinados» para continuar.",
    ),
    ("errUnexpected", "Ocorreu outro erro ao ligar."),
    ("aboutVersion", "Versão"),
    ("aboutBuild", "Compilação"),
    ("aboutServer", "Servidor"),
    ("consoleMissing", "Falta o bundle da consola: compile primeiro a aplicação web."),
];

pub const AR: &[(&str, &str)] = &[
    ("trayOpen", "فتح لوحة التحكم"),
    ("traySettings", "إعدادات الاتصال…"),
    ("trayAbout", "حول"),
    ("trayQuit", "خروج"),
    ("connectTitle", "الاتصال بـ agenthub"),

    ("fieldServer", "عنوان الخادم"),
    ("fieldInsecure", "تجاهل الشهادات الموقّعة ذاتيًا"),
    (
        "fieldInsecureNote",
        "للخوادم داخل شبكتك فقط: يتوقف التطبيق عن التحقق من هوية الطرف الآخر.",
    ),
    ("fieldLanguage", "اللغة"),
    ("actionConnect", "اتصال"),
    ("actionConnecting", "جارٍ الاتصال…"),
    ("actionLater", "لاحقًا"),
    ("actionRetry", "إعادة المحاولة"),
    ("statusChecking", "جارٍ التحقق…"),
    ("statusConnected", "متصل"),
    ("errEmpty", "أدخل عنوان الخادم أولًا."),
    ("errInvalid", "هذا ليس عنوانًا: يجب أن يبدأ بـ http:// أو https://."),
    ("errUnreachable", "لا يمكن الوصول إلى هذا الخادم."),
    (
        "errCertificate",
        "الشهادة غير موثوقة. فعّل «تجاهل الشهادات الموقّعة ذاتيًا» للمتابعة.",
    ),
    ("errUnexpected", "حدث خطأ آخر أثناء الاتصال."),
    ("aboutVersion", "الإصدار"),
    ("aboutBuild", "البناء"),
    ("aboutServer", "الخادم"),
    ("consoleMissing", "حزمة لوحة التحكم مفقودة: ابدأ ببناء تطبيق الويب."),
];

pub const HI: &[(&str, &str)] = &[
    ("trayOpen", "कंसोल खोलें"),
    ("traySettings", "कनेक्शन सेटिंग्स…"),
    ("trayAbout", "परिचय"),
    ("trayQuit", "बंद करें"),
    ("connectTitle", "agenthub से कनेक्ट करें"),

    ("fieldServer", "सर्वर पता"),
    ("fieldInsecure", "स्व-हस्ताक्षरित प्रमाणपत्र अनदेखा करें"),
    (
        "fieldInsecureNote",
        "केवल अपने नेटवर्क के सर्वर के लिए: ऐप यह जांचना बंद कर देता है कि वह किससे बात कर रहा है।",
    ),
    ("fieldLanguage", "भाषा"),
    ("actionConnect", "कनेक्ट करें"),
    ("actionConnecting", "कनेक्ट हो रहा है…"),
    ("actionLater", "बाद में"),
    ("actionRetry", "पुनः प्रयास"),
    ("statusChecking", "जांच हो रही है…"),
    ("statusConnected", "कनेक्ट हो गया"),
    ("errEmpty", "पहले सर्वर का पता दर्ज करें।"),
    ("errInvalid", "यह पता नहीं है: इसे http:// या https:// से शुरू होना चाहिए।"),
    ("errUnreachable", "उस सर्वर तक नहीं पहुँच सकते।"),
    (
        "errCertificate",
        "प्रमाणपत्र विश्वसनीय नहीं है। आगे बढ़ने के लिए «स्व-हस्ताक्षरित प्रमाणपत्र अनदेखा करें» चुनें।",
    ),
    ("errUnexpected", "कनेक्ट करते समय कुछ और गड़बड़ हुई।"),
    ("aboutVersion", "संस्करण"),
    ("aboutBuild", "बिल्ड"),
    ("aboutServer", "सर्वर"),
    ("consoleMissing", "कंसोल बंडल मौजूद नहीं है: पहले वेब ऐप बनाएँ।"),
];

/// The copy block for one language; unknown codes resolve to English.
pub fn block(lang: &str) -> &'static [(&'static str, &'static str)] {
    match lang {
        "zh" => ZH,
        "zh-TW" => ZH_TW,
        "es" => ES,
        "fr" => FR,
        "de" => DE,
        "it" => IT,
        "ru" => RU,
        "ja" => JA,
        "ko" => KO,
        "pt" => PT,
        "ar" => AR,
        "hi" => HI,
        _ => EN,
    }
}

/// Every key the shell translates, in English order.
pub fn keys() -> impl Iterator<Item = &'static str> {
    EN.iter().map(|(key, _)| *key)
}
