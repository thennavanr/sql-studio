// Unit tests for context-aware SQL completion. Run: npm test (Node 22+).
import { sqlColumnCompletion, tokenize, tableRefs, statementBounds } from '../src/sqlcomplete.ts';
const schema = {
  'dbo.Orders': ['id', 'customer_id', 'total', 'status'],
  'dbo.Customers': ['id', 'name', 'email'],
  'sales.Items': ['id', 'sku', 'qty'],
  'dbo.odd name': ['x'],
};
function complete(textWithCursor: string, explicit = false) {
  const pos = textWithCursor.indexOf('|');
  const doc = textWithCursor.replace('|', '');
  const ctx: any = {
    pos, explicit,
    state: { doc: { toString: () => doc } },
    matchBefore(re: RegExp) {
      const before = doc.slice(0, pos);
      const m = new RegExp('(?:' + re.source + ')$').exec(before);
      return m ? { from: pos - m[0].length, to: pos, text: m[0] } : null;
    },
  };
  const r = sqlColumnCompletion(() => schema)(ctx);
  return r ? r.options.map((o: any) => o.apply ?? o.label) : null;
}
let fail = 0;
function check(name: string, got: any, want: any) {
  const ok = JSON.stringify(got) === JSON.stringify(want);
  if (!ok) fail++;
  console.log((ok ? 'PASS ' : 'FAIL ') + name + (ok ? '' : `\n   got:  ${JSON.stringify(got)}\n   want: ${JSON.stringify(want)}`));
}
// WHERE clause after FROM
check('where bare', complete('SELECT * FROM dbo.Orders WHERE st|'), ['id', 'customer_id', 'total', 'status']);
// SELECT list with FROM after the cursor (user's 2nd case)
check('select before from', complete('SELECT cu| FROM Orders'), ['id', 'customer_id', 'total', 'status']);
// alias.
// alias. is completed by the SQL extension; this source stays out of it (no duplicates)
check('alias dot', complete('SELECT o.| FROM dbo.Orders AS o'), null);
check('alias no AS', complete('SELECT * FROM dbo.Orders o JOIN dbo.Customers c ON c.| = o.customer_id'), null);
// join: shared column qualified, unique columns bare
check('join shared qualified', complete('SELECT * FROM Orders o JOIN Customers c ON o.customer_id = c.id WHERE |', true),
  ['o.id', 'customer_id', 'total', 'status', 'c.id', 'name', 'email']);
// comma-separated FROM, schema other than dbo
check('comma from + sales schema', complete('SELECT q| FROM dbo.Orders o, sales.Items i'), ['o.id', 'customer_id', 'total', 'status', 'i.id', 'sku', 'qty']);
// table context: no columns after FROM / JOIN
check('after FROM', complete('SELECT * FROM Or|'), null);
check('after JOIN', complete('SELECT * FROM Orders o JOIN Cu|'), null);
check('after FROM explicit', complete('SELECT * FROM |', true), null);
check('comma in FROM', complete('SELECT * FROM Orders o, Cu|'), null);
// UPDATE ... SET and INSERT INTO
check('update set', complete('UPDATE dbo.Customers SET em| = 1'), ['id', 'name', 'email']);
check('order by', complete('SELECT name FROM Customers ORDER BY n|'), ['id', 'name', 'email']);
// strings and comments
check('inside string', complete("SELECT * FROM Orders WHERE status = 'pa|'"), null);
check('inside comment', complete('SELECT * FROM Orders -- st|'), null);
// statement boundaries: only tables of the current statement
check('other statement ignored', complete('SELECT * FROM Customers;\nSELECT * FROM Orders WHERE t|'), ['id', 'customer_id', 'total', 'status']);
check('blank-line separated', complete('SELECT * FROM Customers\n\nSELECT * FROM Orders WHERE t|'), ['id', 'customer_id', 'total', 'status']);
// unknown table: nothing (lets keywords show)
check('unknown table', complete('SELECT * FROM nope WHERE a|'), null);
// subquery tables and bracketed names
check('subquery', complete('SELECT * FROM Orders WHERE customer_id IN (SELECT id FROM Customers WHERE em|)'), ['Orders.id', 'customer_id', 'total', 'status', 'Customers.id', 'name', 'email']);
check('bracketed', complete('SELECT [x|] FROM [dbo].[odd name]'), ['x']);
check('bracketed table', complete('SELECT x| FROM [dbo].[odd name]'), ['x']);
// plain dbo table qualifier is left to the built-in completion
check('plain table qualifier', complete('SELECT Orders.| FROM Orders'), null);
// tokenize / refs sanity
console.log('refs:', JSON.stringify(tableRefs(tokenize('SELECT * FROM dbo.Orders o LEFT OUTER JOIN Customers AS c ON 1=1 CROSS APPLY sales.Items'), schema)));
console.log('bounds:', JSON.stringify(statementBounds('A;\nB\nGO\nC', 4)));
console.log(fail ? `${fail} FAILED` : "ALL PASSED");
process.exit(fail ? 1 : 0);
