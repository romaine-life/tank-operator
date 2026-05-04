const path = require("node:path");

exports.default = async function afterPack(context) {
  if (context.electronPlatformName !== "win32") return;

  const { rcedit } = await import("rcedit");
  const appInfo = context.packager.appInfo;
  const exePath = path.join(context.appOutDir, `${appInfo.productFilename}.exe`);
  const iconPath = path.join(context.packager.projectDir, "assets", "app-icon.ico");

  await rcedit(exePath, {
    icon: iconPath,
    "requested-execution-level": "asInvoker",
    "file-version": appInfo.buildVersion,
    "product-version": appInfo.version,
    "version-string": {
      CompanyName: "tank-operator",
      FileDescription: "Tank Operator",
      InternalName: "Tank Operator",
      OriginalFilename: "Tank Operator.exe",
      ProductName: "Tank Operator",
    },
  });
};
