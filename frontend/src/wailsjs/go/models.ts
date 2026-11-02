export namespace engine {
	
	export class ColumnInfo {
	    name: string;
	    type: string;
	    nullable: boolean;
	    pk: boolean;
	    identity: boolean;
	    computed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ColumnInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.type = source["type"];
	        this.nullable = source["nullable"];
	        this.pk = source["pk"];
	        this.identity = source["identity"];
	        this.computed = source["computed"];
	    }
	}
	export class DbObject {
	    schema: string;
	    name: string;
	    kind: string;
	
	    static createFrom(source: any = {}) {
	        return new DbObject(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.schema = source["schema"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	    }
	}
	export class ServerInfo {
	    version: string;
	    productLevel: string;
	    edition: string;
	    engineEdition: number;
	    isAzure: boolean;
	    login: string;
	    database: string;
	
	    static createFrom(source: any = {}) {
	        return new ServerInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.productLevel = source["productLevel"];
	        this.edition = source["edition"];
	        this.engineEdition = source["engineEdition"];
	        this.isAzure = source["isAzure"];
	        this.login = source["login"];
	        this.database = source["database"];
	    }
	}

}

export namespace main {
	
	export class ConnectResult {
	    info: engine.ServerInfo;
	    databases: string[];
	
	    static createFrom(source: any = {}) {
	        return new ConnectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.info = this.convertValues(source["info"], engine.ServerInfo);
	        this.databases = source["databases"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FileResult {
	    path: string;
	    name: string;
	    content: string;
	
	    static createFrom(source: any = {}) {
	        return new FileResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.content = source["content"];
	    }
	}

}

export namespace store {
	
	export class Connection {
	    id: string;
	    name: string;
	    server: string;
	    port?: number;
	    database?: string;
	    auth: string;
	    user?: string;
	    tenantId?: string;
	    clientId?: string;
	    encrypt?: string;
	    trustServerCertificate?: boolean;
	    connectTimeout?: number;
	    applicationIntent?: string;
	    color?: string;
	    group?: string;
	    savePassword: boolean;
	    password?: string;
	
	    static createFrom(source: any = {}) {
	        return new Connection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.server = source["server"];
	        this.port = source["port"];
	        this.database = source["database"];
	        this.auth = source["auth"];
	        this.user = source["user"];
	        this.tenantId = source["tenantId"];
	        this.clientId = source["clientId"];
	        this.encrypt = source["encrypt"];
	        this.trustServerCertificate = source["trustServerCertificate"];
	        this.connectTimeout = source["connectTimeout"];
	        this.applicationIntent = source["applicationIntent"];
	        this.color = source["color"];
	        this.group = source["group"];
	        this.savePassword = source["savePassword"];
	        this.password = source["password"];
	    }
	}
	export class HistoryEntry {
	    // Go type: time
	    time: any;
	    connectionId: string;
	    database: string;
	    query: string;
	    durationMs: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new HistoryEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = this.convertValues(source["time"], null);
	        this.connectionId = source["connectionId"];
	        this.database = source["database"];
	        this.query = source["query"];
	        this.durationMs = source["durationMs"];
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Settings {
	    theme: string;
	    fontSize: number;
	    maxRows: number;
	    queryTimeout: number;
	    historyEnabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.fontSize = source["fontSize"];
	        this.maxRows = source["maxRows"];
	        this.queryTimeout = source["queryTimeout"];
	        this.historyEnabled = source["historyEnabled"];
	    }
	}

}

