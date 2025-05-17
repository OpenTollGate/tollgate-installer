// Linux Sandbox Fix
// This script runs during the electron-builder afterPack hook for Linux builds
// It ensures the chrome-sandbox has the correct permissions to run properly

const fs = require('fs');
const path = require('path');
const childProcess = require('child_process');

exports.default = async function(context) {
  // Only run on Linux platform
  if (process.platform !== 'linux') {
    return;
  }

  const appOutDir = context.appOutDir;
  console.log(`Running Linux sandbox fix in: ${appOutDir}`);

  try {
    // Path to chrome-sandbox in the unpacked app
    const sandboxPath = path.join(appOutDir, 'node_modules/electron/dist/chrome-sandbox');
    
    if (fs.existsSync(sandboxPath)) {
      // Try to set the correct permissions using chmod
      console.log(`Setting permissions on: ${sandboxPath}`);
      try {
        // Set the permissions to 4755 (SUID)
        childProcess.execSync(`chmod 4755 "${sandboxPath}"`);
        console.log('Successfully set chrome-sandbox permissions');
      } catch (error) {
        console.warn('Could not set permissions automatically:', error.message);
        console.warn('After installation, you may need to set permissions manually:');
        console.warn(`sudo chown root:root "${sandboxPath}"`);
        console.warn(`sudo chmod 4755 "${sandboxPath}"`);
      }
    } else {
      console.warn(`Chrome sandbox not found at: ${sandboxPath}`);
    }
  } catch (error) {
    console.error('Error in Linux sandbox fix:', error);
  }
};