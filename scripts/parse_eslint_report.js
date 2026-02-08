
const fs = require('fs');
const path = require('path');

const reportPath = path.join(__dirname, '../frontend/eslint-report.json');
const outputPath = path.join(__dirname, '../frontend/i18n-report.txt');

try {
  if (!fs.existsSync(reportPath)) {
    // If report doesn't exist, it might be because ESLint didn't find any errors (exit code 0) 
    // or failed critically. We'll check if we can proceed.
    console.log('No ESLint report found. This might mean no errors were detected or ESLint failed to run.');
    process.exit(0);
  }

  const content = fs.readFileSync(reportPath, 'utf8');
  const results = JSON.parse(content);

  let totalStrings = 0;
  let outputLines = [];

  outputLines.push('--- Hardcoded Strings Report ---');
  outputLines.push(`Generated: ${new Date().toLocaleString()}\n`);

  results.forEach(result => {
    const relativePath = path.relative(path.join(__dirname, '../frontend'), result.filePath);
    const messages = result.messages.filter(m => m.ruleId === '@intlify/vue-i18n/no-raw-text');

    if (messages.length > 0) {
      outputLines.push(`File: ${relativePath}`);
      messages.forEach(m => {
        outputLines.push(`  Line ${m.line}: "${m.message.replace("raw text '", "").replace("' is found", "")}"`);
        totalStrings++;
      });
      outputLines.push('');
    }
  });

  outputLines.push(`Total hardcoded strings found: ${totalStrings}`);
  
  // Write to file
  fs.writeFileSync(outputPath, outputLines.join('\n'), 'utf8');
  console.log(`\n✅ Report saved to: ${outputPath}`);
  console.log(`Total strings: ${totalStrings}`);

  // Cleanup intermediate file
  try {
    fs.unlinkSync(reportPath);
    console.log(`🗑️  Deleted intermediate file: ${path.basename(reportPath)}`);
  } catch (err) {
    console.warn('Warning: Failed to delete intermediate report file.');
  }

} catch (e) {
  console.error('Error parsing report:', e.message);
  process.exit(1);
}
