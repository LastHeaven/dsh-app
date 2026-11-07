// dsh web 托盘版 —— dsh-web-restart.cmd 的 exe 封装
//
// 常驻右下角通知区，用鲸鱼图标表示状态：
//   黑色 = 运行中   琥珀色 = 启动中/异常   灰色 = 未运行
// 左键点击 = 打开管理界面；右键 = 启动 / 停止 / 重启 / 打开目录 / 退出。
//
// 命令行参数（供脚本、计划任务调用，跑完即退，不显示托盘）：
//   -Port 8080  -NoBrowser  -KeepExisting  -Stop  -DryRun  -Headless  -Help
using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.Linq;
using System.Management;
using System.Net.NetworkInformation;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using System.Windows.Forms;

namespace DshWebLauncher
{
    internal static class Program
    {
        [STAThread]
        private static int Main(string[] args)
        {
            // 必须在任何输出之前接管父控制台，否则中文乱码、输出跑到错误的句柄上
            ConsoleShim.AttachToParent();

            var opt = Options.Parse(args);

            if (opt.ShowHelp)
            {
                Options.PrintHelp();
                return 0;
            }

            // 运维动作：跑完就退，不构造托盘
            if (opt.DryRun || opt.StopOnly || opt.Headless)
                return Headless.Run(opt);

            // 单实例：已有托盘在跑就把命令转交给它，避免出现第二个图标
            if (SingleInstance.TryForward(args))
                return 0;

            Application.EnableVisualStyles();
            Application.SetCompatibleTextRenderingDefault(false);

            var app = new TrayApp(opt);
            app.Run();
            return 0;
        }
    }

    // ------------------------------------------------------------------ 参数

    internal sealed class Options
    {
        public int Port = 3080;
        public bool NoBrowser;
        public bool KeepExisting;
        public bool StopOnly;
        public bool DryRun;
        public bool Headless;
        public bool ShowHelp;

        public static Options Parse(string[] args)
        {
            var o = new Options();
            for (int i = 0; i < args.Length; i++)
            {
                var key = args[i].TrimStart('-', '/').ToLowerInvariant();
                switch (key)
                {
                    case "port":
                        if (i + 1 < args.Length && int.TryParse(args[++i], out int p)) o.Port = p;
                        break;
                    case "nobrowser": o.NoBrowser = true; break;
                    case "keepexisting": o.KeepExisting = true; break;
                    case "stop": o.StopOnly = true; break;
                    case "dryrun": o.DryRun = true; break;
                    case "headless": o.Headless = true; break;
                    case "h":
                    case "help":
                    case "?": o.ShowHelp = true; break;
                    default:
                        if (key.StartsWith("port:") && int.TryParse(key.Substring(5), out int p2)) o.Port = p2;
                        break;
                }
            }
            return o;
        }

        public static void PrintHelp()
        {
            var text = @"dsh web 托盘版

  双击 dsh-web.exe          在右下角显示状态图标，并启动 dsh web
  dsh-web.exe -Stop         只关闭该端口上的 dsh web，不启动
  dsh-web.exe -DryRun       只检测并输出，不做任何改动
  dsh-web.exe -Port 8080    使用其它端口（停止也只影响该端口）
  dsh-web.exe -NoBrowser    启动后不自动打开浏览器
  dsh-web.exe -KeepExisting 不关闭已有实例（配合 -Port 调试用）
  dsh-web.exe -Headless     不显示托盘，按命令行跑完就退出（等同原 cmd 行为）

托盘菜单：打开管理界面 / 启动 / 停止 / 重启 / 打开访问地址
         打开安装目录 / 查看日志 / 退出";
            Console.WriteLine(text);
        }
    }

    // ------------------------------------------------------------------ 路径与公共工具

    internal static class Paths
    {
        public static string Root
        {
            get
            {
                var exe = Environment.ProcessPath ?? Process.GetCurrentProcess().MainModule.FileName;
                var dir = Path.GetDirectoryName(exe) ?? Directory.GetCurrentDirectory();
                if (File.Exists(Path.Combine(dir, "dsh-web-restart.ps1")) ||
                    File.Exists(Path.Combine(dir, "last-url.txt")))
                    return dir;

                const string fallback = @"D:\agents\dsh";
                return Directory.Exists(fallback) ? fallback : dir;
            }
        }

        public static string LogDir => Path.Combine(Root, "logs");
        public static string OutLog => Path.Combine(LogDir, "dsh-web.out.log");
        public static string ErrLog => Path.Combine(LogDir, "dsh-web.err.log");
        public static string UrlFile => Path.Combine(Root, "last-url.txt");

        public static string SafeRead(string p)
        {
            try { return File.Exists(p) ? File.ReadAllText(p) : ""; } catch { return ""; }
        }

        public static void TryDelete(string p)
        {
            try { if (File.Exists(p)) File.Delete(p); } catch { }
        }

        public static string Trim(string s, int max)
        {
            if (string.IsNullOrEmpty(s)) return "";
            s = s.Trim();
            return s.Length <= max ? s : s.Substring(0, max) + "...";
        }

        public static bool IsPortListening(int port)
        {
            try
            {
                return IPGlobalProperties.GetIPGlobalProperties()
                    .GetActiveTcpListeners().Any(ep => ep.Port == port);
            }
            catch { return false; }
        }

        // 用 shell 代开，浏览器保持普通权限（本程序是提权运行的）
        public static void OpenUrl(string url)
        {
            if (string.IsNullOrWhiteSpace(url)) return;
            try
            {
                Process.Start(new ProcessStartInfo("explorer.exe", "\"" + url + "\"")
                { UseShellExecute = true });
            }
            catch
            {
                try { Process.Start(new ProcessStartInfo(url) { UseShellExecute = true }); } catch { }
            }
        }

        public static void OpenFolder(string path)
        {
            try
            {
                if (!Directory.Exists(path)) Directory.CreateDirectory(path);
                Process.Start(new ProcessStartInfo("explorer.exe", "\"" + path + "\"")
                { UseShellExecute = true });
            }
            catch { }
        }

        public static string ResolveNode()
        {
            foreach (var p in (Environment.GetEnvironmentVariable("PATH") ?? "").Split(';'))
            {
                if (string.IsNullOrWhiteSpace(p)) continue;
                try
                {
                    var c = Path.Combine(p.Trim('"'), "node.exe");
                    if (File.Exists(c)) return c;
                }
                catch { }
            }
            var cands = new[]
            {
                Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.ProgramFiles), @"nodejs\node.exe"),
                Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.ProgramFilesX86), @"nodejs\node.exe"),
                Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), @"Programs\nodejs\node.exe")
            };
            return cands.FirstOrDefault(File.Exists);
        }

        public static string ResolveDshBin()
        {
            const string rel = @"node_modules\@deepseek-ai\dsh\lib\bin.js";
            var roots = new List<string>();

            var path = (Environment.GetEnvironmentVariable("PATH") ?? "").Split(';');
            foreach (var name in new[] { "dsh.cmd", "dsh.ps1", "dsh.bat", "dsh.exe", "dsh" })
            {
                foreach (var raw in path)
                {
                    if (string.IsNullOrWhiteSpace(raw)) continue;
                    try
                    {
                        var dir = raw.Trim('"');
                        if (!File.Exists(Path.Combine(dir, name))) continue;
                        var c = Path.Combine(dir, rel);
                        if (File.Exists(c)) return c;
                        var parent = Path.GetDirectoryName(dir);
                        if (!string.IsNullOrEmpty(parent))
                        {
                            var c2 = Path.Combine(parent, rel);
                            if (File.Exists(c2)) return c2;
                        }
                    }
                    catch { }
                }
            }

            try
            {
                var psi = new ProcessStartInfo("npm", "root -g")
                {
                    UseShellExecute = false,
                    CreateNoWindow = true,
                    RedirectStandardOutput = true,
                    RedirectStandardError = true
                };
                using (var p = Process.Start(psi))
                {
                    var outp = p.StandardOutput.ReadToEnd();
                    p.WaitForExit(8000);
                    var line = outp.Split('\n').Select(x => x.Trim()).FirstOrDefault(x => x.Length > 0);
                    if (!string.IsNullOrEmpty(line)) roots.Add(line);
                }
            }
            catch { }

            roots.Add(Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData), "npm"));
            roots.Add(Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.ProgramFiles), "nodejs"));
            roots.Add(@"D:\npm\node_global");

            foreach (var r in roots)
            {
                if (string.IsNullOrWhiteSpace(r)) continue;
                try
                {
                    var c = Path.Combine(r, rel);
                    if (File.Exists(c)) return c;
                }
                catch { }
            }
            return null;
        }
    }

    // ------------------------------------------------------------------ 进程探测与启停

    internal static class DshService
    {
        private static readonly Regex BinPattern =
            new Regex(@"(?i)@deepseek-ai[\\/]dsh[\\/]lib[\\/]bin\.js", RegexOptions.Compiled);
        private static readonly Regex WebPattern =
            new Regex(@"(?i)(^|\s)web(\s|$)", RegexOptions.Compiled);

        // 一次性把 node 进程的 pid / 父 pid / 命令行抓进快照。
        // 同一进程里反复 new ManagementObjectSearcher 会不稳定，所以只查一次。
        private static List<(int Pid, int Parent, string Cmd)> _snapshot;

        private static List<(int Pid, int Parent, string Cmd)> Snapshot()
        {
            if (_snapshot != null) return _snapshot;

            var list = new List<(int, int, string)>();
            try
            {
                using (var searcher = new ManagementObjectSearcher(
                    "SELECT ProcessId, ParentProcessId, Name, CommandLine FROM Win32_Process"))
                using (var rows = searcher.Get())
                {
                    foreach (ManagementObject row in rows)
                    {
                        try
                        {
                            var name = row["Name"] as string ?? "";
                            if (!string.Equals(name, "node.exe", StringComparison.OrdinalIgnoreCase)) continue;
                            list.Add((Convert.ToInt32(row["ProcessId"]),
                                      Convert.ToInt32(row["ParentProcessId"]),
                                      row["CommandLine"] as string ?? ""));
                        }
                        catch { }
                        finally { row.Dispose(); }
                    }
                }
            }
            catch { }

            _snapshot = list;
            return list;
        }

        public static void InvalidateSnapshot() => _snapshot = null;

        // node.exe 且命令行同时含 bin.js 与独立的 web 参数（不会误伤 CLI 会话）
        public static Process[] LiveWeb()
        {
            var list = new List<Process>();
            foreach (var (pid, _, cmd) in Snapshot())
            {
                try
                {
                    if (!BinPattern.IsMatch(cmd) || !WebPattern.IsMatch(cmd)) continue;
                    list.Add(Process.GetProcessById(pid));
                }
                catch { }
            }
            return list.ToArray();
        }

        public static bool IsRunning => LiveWeb().Length > 0;

        // 停掉占用指定端口的那个 dsh web 实例，返回结束的进程数。
        //
        // 必须按端口精确定位：早先"杀掉所有 dsh web"的写法，
        // 在 -Port 3199 隔离测试时把 3080 上正在用的实例也一起杀了。
        public static int Stop(int port)
        {
            InvalidateSnapshot();
            var live = LiveWeb();
            if (live.Length == 0) return 0;

            var targets = new List<Process>();

            var ownerPid = PortOwnerPid(port);
            if (ownerPid > 0)
            {
                var hit = live.FirstOrDefault(p => p.Id == ownerPid);
                if (hit != null) targets.Add(hit);
            }

            // 端口已无归属（进程刚退出）时，若全机只有一个 web 实例，就停它
            if (targets.Count == 0 && live.Length == 1) targets.Add(live[0]);
            if (targets.Count == 0) return 0;

            var pids = targets.Select(p => p.Id).ToArray();
            foreach (var p in targets)
            {
                try { p.Kill(); } catch { }
            }

            var until = DateTime.UtcNow.AddSeconds(20);
            while (DateTime.UtcNow < until && AnyAlive(targets.ToArray()))
            {
                try { Thread.Sleep(300); } catch { }
            }

            foreach (var pid in pids)
            {
                try
                {
                    using (var p = Process.GetProcessById(pid))
                        if (!p.HasExited) p.Kill();
                }
                catch { }
            }

            InvalidateSnapshot();
            return targets.Count;
        }

        private static bool AnyAlive(Process[] procs)
        {
            foreach (var p in procs)
            {
                try { if (!p.HasExited) return true; } catch { }
            }
            return false;
        }

        public static int PortOwnerPid(int port) => NativePortOwner(port);

        [DllImport("iphlpapi.dll", SetLastError = true)]
        private static extern uint GetExtendedTcpTable(
            IntPtr pTcpTable, ref int dwOutBufLen, bool sort, int ipVersion, int tblClass, uint reserved);

        private const int AfInet = 2;
        private const int TcpTableOwnerPidAll = 5;

        // MIB_TCPROW_OWNER_PID：每行 24 字节，表头 4 字节计数；行内布局
        //   dwState(0) dwLocalAddr(4) dwLocalPort(8) dwRemoteAddr(12)
        //   dwRemotePort(16) dwOwningPid(20)
        // 端口号高字节序存在低 16 位，必须换字节序后再比较。
        private static int NativePortOwner(int port)
        {
            int size = 0;
            try
            {
                GetExtendedTcpTable(IntPtr.Zero, ref size, false, AfInet, TcpTableOwnerPidAll, 0);
                if (size <= 0) return 0;

                var buf = Marshal.AllocHGlobal(size);
                try
                {
                    if (GetExtendedTcpTable(buf, ref size, false, AfInet, TcpTableOwnerPidAll, 0) != 0)
                        return 0;

                    int rows = Marshal.ReadInt32(buf);
                    for (int i = 0; i < rows; i++)
                    {
                        IntPtr row = IntPtr.Add(buf, 4 + i * 24);
                        int rawPort = Marshal.ReadInt32(row, 8);
                        int localPort = ((rawPort & 0xFF) << 8) | ((rawPort >> 8) & 0xFF);
                        if (localPort != port) continue;

                        int pid = Marshal.ReadInt32(row, 20);
                        if (pid > 0) return pid;
                    }
                }
                finally { Marshal.FreeHGlobal(buf); }
            }
            catch { }
            return 0;
        }

        // 后台启动，返回带 token 的访问地址；失败抛异常
        public static string Start(Options opt)
        {
            // 只有目标端口确实被占着时才停掉那一个实例，不影响其它端口
            if (Paths.IsPortListening(opt.Port) && !opt.KeepExisting)
                Stop(opt.Port);

            var node = Paths.ResolveNode() ?? throw new InvalidOperationException(
                "未找到 node.exe，请确认 Node.js 已安装并加入 PATH。");
            var bin = Paths.ResolveDshBin() ?? throw new InvalidOperationException(
                "未找到 dsh 入口 bin.js，请确认已执行：npm i -g @deepseek-ai/dsh");

            var wait = DateTime.UtcNow.AddSeconds(20);
            while (DateTime.UtcNow < wait && Paths.IsPortListening(opt.Port)) Thread.Sleep(300);
            if (Paths.IsPortListening(opt.Port))
                throw new InvalidOperationException($"端口 {opt.Port} 仍被其它程序占用。");

            Directory.CreateDirectory(Paths.LogDir);
            Paths.TryDelete(Paths.OutLog);
            Paths.TryDelete(Paths.ErrLog);

            var psi = new ProcessStartInfo
            {
                FileName = node,
                UseShellExecute = false,
                CreateNoWindow = true,
                WindowStyle = ProcessWindowStyle.Hidden,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                RedirectStandardInput = true
            };
            psi.ArgumentList.Add(bin);
            psi.ArgumentList.Add("web");
            psi.ArgumentList.Add("--port");
            psi.ArgumentList.Add(opt.Port.ToString());
            psi.ArgumentList.Add("--no-open");

            var proc = Process.Start(psi) ?? throw new InvalidOperationException("无法创建 node 进程。");

            var sb = new StringBuilder();
            var gate = new object();
            var outWriter = new StreamWriter(Paths.OutLog, false, new UTF8Encoding(false)) { AutoFlush = true };
            var errWriter = new StreamWriter(Paths.ErrLog, false, new UTF8Encoding(false)) { AutoFlush = true };

            proc.OutputDataReceived += (s, e) =>
            {
                if (e.Data == null) return;
                lock (gate) sb.AppendLine(e.Data);
                try { outWriter.WriteLine(e.Data); } catch { }
            };
            proc.ErrorDataReceived += (s, e) =>
            {
                if (e.Data == null) return;
                lock (gate) sb.AppendLine(e.Data);
                try { errWriter.WriteLine(e.Data); } catch { }
            };
            proc.EnableRaisingEvents = true;
            proc.Exited += (s, e) => { try { outWriter.Flush(); errWriter.Flush(); } catch { } };
            proc.BeginOutputReadLine();
            proc.BeginErrorReadLine();

            // dsh web 启动时会打印带 token 的访问地址，从这里抓
            string url = null;
            var deadline = DateTime.UtcNow.AddSeconds(120);
            while (DateTime.UtcNow < deadline)
            {
                if (proc.HasExited) break;
                lock (gate)
                {
                    var m = Regex.Match(sb.ToString(), @"https?://[^\s""']+");
                    if (m.Success) { url = m.Value; break; }
                }
                Thread.Sleep(500);
            }

            if (proc.HasExited && url == null)
            {
                var o = Paths.SafeRead(Paths.OutLog);
                var e = Paths.SafeRead(Paths.ErrLog);
                throw new InvalidOperationException(
                    $"dsh web 启动后立即退出（退出码 {proc.ExitCode}）：{Paths.Trim(o + "\n" + e, 300)}");
            }
            if (url == null)
                throw new InvalidOperationException("等待 dsh web 输出访问地址超时，请查看 logs 目录。");

            var upWait = DateTime.UtcNow.AddSeconds(30);
            while (DateTime.UtcNow < upWait && !Paths.IsPortListening(opt.Port)) Thread.Sleep(300);

            try { File.WriteAllText(Paths.UrlFile, url, Encoding.ASCII); } catch { }

            if (!opt.NoBrowser) Paths.OpenUrl(url);
            return url;
        }

        // 最近一次启动写入的地址；没有就读日志里最后一个。
        //
        // 必须校验端口：last-url.txt 是历史遗留文件，可能还留着别的端口的地址
        // （调试时用 -Port 3199 起过实例就会写进去）。不校验的话，托盘会打开一个
        // 早已失效的端口，表现为"点开页面却是 3199"。
        public static string LastUrl(int port)
        {
            try
            {
                if (File.Exists(Paths.UrlFile))
                {
                    var last = File.ReadAllText(Paths.UrlFile).Trim();
                    if (UrlMatchesPort(last, port)) return last;
                }
            }
            catch { }

            // 文件没写或端口不符，从日志里找该端口的最后一个地址（倒序找）
            try
            {
                if (File.Exists(Paths.OutLog))
                {
                    var all = Regex.Matches(File.ReadAllText(Paths.OutLog), @"https?://[^\s""']+");
                    for (int i = all.Count - 1; i >= 0; i--)
                    {
                        if (UrlMatchesPort(all[i].Value, port)) return all[i].Value;
                    }
                }
            }
            catch { }
            return null;
        }

        // 地址里的端口是否就是我们要的那个
        private static bool UrlMatchesPort(string url, int port)
        {
            if (string.IsNullOrWhiteSpace(url)) return false;
            try
            {
                var u = new Uri(url);
                return u.Port == port;
            }
            catch { return false; }
        }
    }

    // ------------------------------------------------------------------ 无界面模式

    internal static class Headless
    {
        // 控制台句柄可能失效，关键信息同时写一份文件便于排查
        private static void Say(string text)
        {
            try { Console.WriteLine(text); } catch { }
            try
            {
                File.AppendAllText(Path.Combine(Paths.Root, "headless.log"),
                    DateTime.Now.ToString("HH:mm:ss") + "  " + text + Environment.NewLine,
                    new UTF8Encoding(false));
            }
            catch { }
        }

        public static int Run(Options opt)
        {
            try
            {
                Say("[启动] 参数 Port=" + opt.Port +
                    " DryRun=" + opt.DryRun + " Stop=" + opt.StopOnly +
                    " NoBrowser=" + opt.NoBrowser + " KeepExisting=" + opt.KeepExisting);

                if (opt.DryRun)
                {
                    var live = DshService.LiveWeb();
                    Say(live.Length > 0
                        ? "检测到 dsh web：" + string.Join(", ", live.Select(p => "PID " + p.Id))
                        : "未检测到已运行的 dsh web。");
                    return 0;
                }

                if (opt.StopOnly)
                {
                    var n = DshService.Stop(opt.Port);
                    Say(n > 0
                        ? $"dsh web 已关闭（端口 {opt.Port}，结束 {n} 个进程）。"
                        : $"端口 {opt.Port} 上没有运行中的 dsh web。");
                    return 0;
                }

                var url = DshService.Start(opt);
                Say("dsh web 已启动：" + url);
                return 0;
            }
            catch (Exception ex)
            {
                Say("启动失败：" + ex.GetType().Name + ": " + ex.Message);
                try { Console.Error.WriteLine("启动失败：" + ex.Message); } catch { }
                return 1;
            }
        }
    }

    // ------------------------------------------------------------------ 托盘

    internal sealed class TrayApp
    {
        private const string Title = "dsh web";

        private readonly Options _opt;
        private readonly NotifyIcon _icon;
        private readonly System.Windows.Forms.Timer _poller;
        private readonly Icon _iconRunning, _iconStopped, _iconBusy;

        private string _url;
        private bool _busy;
        private bool _wasRunning;
        private bool _exiting;

        public TrayApp(Options opt)
        {
            _opt = opt;

            _iconRunning = LoadIcon("running.ico") ?? SystemIcons.Application;
            _iconStopped = LoadIcon("stopped.ico") ?? SystemIcons.Application;
            _iconBusy = LoadIcon("busy.ico") ?? SystemIcons.Application;

            _icon = new NotifyIcon
            {
                Icon = _iconStopped,
                Text = Title,
                Visible = true,
                ContextMenuStrip = BuildMenu()
            };
            _icon.MouseClick += (s, e) =>
            {
                if (e.Button == MouseButtons.Left) _ = OpenUiAsync();
            };

            SingleInstance.CommandReceived = OnForwardedCommand;

            _poller = new System.Windows.Forms.Timer { Interval = 3000 };
            _poller.Tick += (s, e) => RefreshState();
            _poller.Start();

            RefreshState();

            // 首次启动：本端口的服务没在跑就拉起来
            if (!Paths.IsPortListening(_opt.Port) && !_opt.StopOnly)
                _ = StartAsync();
        }

        private ContextMenuStrip BuildMenu()
        {
            var menu = new ContextMenuStrip { Font = new Font("Microsoft YaHei UI", 9f) };
            menu.Items.Add(Item("打开管理界面", async (s, e) => await OpenUiAsync()));
            menu.Items.Add(new ToolStripSeparator());
            menu.Items.Add(Item("启动", async (s, e) => await StartAsync()));
            menu.Items.Add(Item("停止", async (s, e) => await StopAsync()));
            menu.Items.Add(Item("重启", async (s, e) => await RestartAsync()));
            menu.Items.Add(new ToolStripSeparator());
            menu.Items.Add(Item("打开访问地址", (s, e) => Paths.OpenUrl(_url ?? DshService.LastUrl(_opt.Port))));
            menu.Items.Add(Item("打开安装目录", (s, e) => Paths.OpenFolder(Paths.Root)));
            menu.Items.Add(Item("查看日志", (s, e) => Paths.OpenFolder(Paths.LogDir)));
            menu.Items.Add(new ToolStripSeparator());
            menu.Items.Add(Item("退出", (s, e) => Exit()));
            return menu;
        }

        private static ToolStripMenuItem Item(string text, EventHandler onClick)
        {
            var it = new ToolStripMenuItem(text);
            it.Click += onClick;
            return it;
        }

        private static Icon LoadIcon(string name)
        {
            // 图标以 EmbeddedResource 编进程序集，从清单流读取
            try
            {
                var asm = typeof(TrayApp).Assembly;
                var res = asm.GetManifestResourceNames()
                    .FirstOrDefault(n => n.EndsWith("." + name, StringComparison.OrdinalIgnoreCase));
                if (res != null)
                {
                    using (var s = asm.GetManifestResourceStream(res))
                        if (s != null) return new Icon(s);
                }
            }
            catch { }

            try
            {
                var dir = Path.GetDirectoryName(Environment.ProcessPath
                          ?? Process.GetCurrentProcess().MainModule.FileName);
                var f = Path.Combine(dir ?? "", "assets", name);
                if (File.Exists(f)) return new Icon(f);
            }
            catch { }
            return null;
        }

        // -------------------------------------------------------------- 状态显示

        private void RefreshState()
        {
            if (_exiting) return;

            DshService.InvalidateSnapshot();
            var running = Paths.IsPortListening(_opt.Port);
            var live = DshService.LiveWeb();

            if (running && !_busy)
            {
                _icon.Icon = _iconRunning;
                SetText($"dsh web：运行中\n端口 {_opt.Port}");
            }
            else if (!running && !_busy)
            {
                _icon.Icon = _iconStopped;
                SetText("dsh web：未运行");
            }

            if (_wasRunning && !running && !_busy && !_exiting)
            {
                _icon.ShowBalloonTip(4000, Title, "dsh web 已退出。右键图标可重新启动。",
                    ToolTipIcon.Warning);
            }
            _wasRunning = running;
        }

        // NotifyIcon.Text 上限 63 字符，超了会抛异常
        private void SetText(string text)
        {
            try { _icon.Text = text.Length > 63 ? text.Substring(0, 63) : text; }
            catch { }
        }

        // -------------------------------------------------------------- 动作

        private void OnForwardedCommand(string argLine)
        {
            var args = (argLine ?? "").Split(new[] { ' ' }, StringSplitOptions.RemoveEmptyEntries);
            var opt = Options.Parse(args);

            if (opt.StopOnly) { _ = StopAsync(); return; }
            if (opt.DryRun) { _ = OpenUiAsync(); return; }

            _opt.Port = opt.Port;
            _opt.NoBrowser = opt.NoBrowser;
            _opt.KeepExisting = opt.KeepExisting;
            _ = RestartAsync();
        }

        private async Task StartAsync()
        {
            if (_busy) return;
            _busy = true;
            _icon.Icon = _iconBusy;
            SetText("dsh web：正在启动...");

            try
            {
                _url = await Task.Run(() =>
                {
                    DshService.InvalidateSnapshot();
                    return DshService.Start(_opt);
                });
            }
            catch (Exception ex)
            {
                _url = null;
                _icon.ShowBalloonTip(8000, Title, "启动失败：" + ex.Message, ToolTipIcon.Error);
            }
            finally
            {
                _busy = false;
                DshService.InvalidateSnapshot();
                RefreshState();
            }
        }

        private async Task StopAsync()
        {
            if (_busy) return;
            _busy = true;
            _icon.Icon = _iconBusy;
            SetText("dsh web：正在停止...");

            try
            {
                var n = await Task.Run(() => DshService.Stop(_opt.Port));
                _url = null;
                _icon.ShowBalloonTip(2500, Title,
                    n > 0 ? "dsh web 已停止。" : "dsh web 未在运行。", ToolTipIcon.Info);
            }
            finally
            {
                _busy = false;
                _wasRunning = false;   // 手动停止，别再当成意外退出提示一次
                DshService.InvalidateSnapshot();
                RefreshState();
            }
        }

        private async Task RestartAsync()
        {
            await StopAsync();
            await StartAsync();
        }

        private async Task OpenUiAsync()
        {
            // 只认本端口的地址，历史遗留的别的端口要忽略
            var url = _url ?? DshService.LastUrl(_opt.Port);
            if (url != null)
            {
                Paths.OpenUrl(url);
                return;
            }

            if (!Paths.IsPortListening(_opt.Port))
            {
                _icon.ShowBalloonTip(3000, Title, "dsh web 未运行，正在启动...", ToolTipIcon.Info);
                await StartAsync();
                var fresh = _url ?? DshService.LastUrl(_opt.Port);
                if (fresh != null) Paths.OpenUrl(fresh);
            }
            else
            {
                _icon.ShowBalloonTip(4000, Title,
                    "服务在运行，但还没拿到带 token 的地址，请稍等几秒再点。", ToolTipIcon.Warning);
            }
        }

        private void Exit()
        {
            _exiting = true;
            _poller.Stop();
            _icon.Visible = false;
            _icon.Dispose();
            Application.Exit();
        }

        public void Run()
        {
            Application.Run();
        }
    }

    // ------------------------------------------------------------------ 单实例

    // 第二个实例不建第二个图标，而是通过命名管道把命令交给第一个实例
    internal static class SingleInstance
    {
        private const string PipeName = "dsh-web-tray-{7E2F5C41-9B3A-4E52-8C1D-2A6F0B4D9E13}";

        public static Action<string> CommandReceived;

        public static bool TryForward(string[] args)
        {
            Mutex mutex = null;
            bool created = false;
            try
            {
                mutex = new Mutex(true, @"Global\" + PipeName, out created);
                if (created)
                {
                    GC.KeepAlive(mutex);   // 存活到进程结束
                    StartListener();
                    return false;
                }
            }
            catch { return false; }
            finally
            {
                if (!created) { try { mutex?.Dispose(); } catch { } }
            }

            try
            {
                using (var client = new System.IO.Pipes.NamedPipeClientStream(
                           ".", PipeName, System.IO.Pipes.PipeDirection.Out))
                {
                    client.Connect(2500);
                    using (var w = new StreamWriter(client, Encoding.UTF8) { AutoFlush = true })
                        w.WriteLine(string.Join(" ", args));
                }
                return true;
            }
            catch
            {
                MessageBox.Show("dsh web 已在托盘中运行，请查看右下角通知区域。",
                    "dsh web", MessageBoxButtons.OK, MessageBoxIcon.Information);
                return true;
            }
        }

        private static void StartListener()
        {
            var t = new Thread(() =>
            {
                while (true)
                {
                    try
                    {
                        using (var server = new System.IO.Pipes.NamedPipeServerStream(
                                   PipeName, System.IO.Pipes.PipeDirection.In, 1,
                                   System.IO.Pipes.PipeTransmissionMode.Byte,
                                   System.IO.Pipes.PipeOptions.None))
                        {
                            server.WaitForConnection();
                            using (var r = new StreamReader(server, Encoding.UTF8))
                            {
                                var line = r.ReadLine() ?? "";
                                CommandReceived?.Invoke(line.Trim());
                            }
                        }
                    }
                    catch { Thread.Sleep(300); }
                }
            })
            { IsBackground = true, Name = "dsh-web-ipc" };
            t.Start();
        }
    }

    // ------------------------------------------------------------------ 控制台

    internal static class ConsoleShim
    {
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool AttachConsole(int pid);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern IntPtr GetStdHandle(int nStdHandle);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern uint GetFileType(IntPtr hFile);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern uint GetConsoleOutputCP();

        private const int StdOutputHandle = -11;
        private const uint FileTypePipe = 3;

        // WinExe 没有自带控制台。被 cmd/PowerShell 调用时附着到父控制台：
        // 输出重定向到管道时用 UTF-8，直接写真实控制台时用控制台自身代码页。
        public static void AttachToParent()
        {
            try
            {
                var handle = GetStdHandle(StdOutputHandle);
                if (handle == IntPtr.Zero) AttachConsole(-1);

                handle = GetStdHandle(StdOutputHandle);
                var enc = Encoding.UTF8;
                if (handle != IntPtr.Zero && GetFileType(handle) != FileTypePipe)
                {
                    try
                    {
                        var cp = (int)GetConsoleOutputCP();
                        if (cp > 0) enc = Encoding.GetEncoding(cp);
                    }
                    catch { }
                }

                Console.SetOut(new StreamWriter(Console.OpenStandardOutput(), enc) { AutoFlush = true });
                Console.SetError(new StreamWriter(Console.OpenStandardError(), enc) { AutoFlush = true });
            }
            catch { }
        }
    }
}
