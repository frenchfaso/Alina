# Spunti per Alina — 8 settembre 2026

Il principio resta **less is more**. Questi lavori informano il disegno del POC;
non dimostrano che Alina migliori automaticamente con il tempo.

| Riferimento | Spunto utile | Scelta per Alina |
| --- | --- | --- |
| [Generative Agents, Park et al., 2023](https://arxiv.org/html/2304.03442v2#S4.SS2) | Le riflessioni citano le osservazioni da cui derivano; la ricerca combina rilevanza, recenza e importanza. Gli autori osservano anche ricordi abbelliti o inventati. | Nuclei piccoli con fonti verificabili, date e istruzioni a distinguere fatti da interpretazioni. Nessun albero ricorsivo di riflessioni nel POC. |
| [MemGPT, Packer et al., 2023](https://arxiv.org/html/2310.08560v2#S2) | Il contesto del modello è limitato; la memoria esterna viene richiamata attraverso funzioni. | Diario e settimana con un budget di contesto, archivio cercabile attraverso un unico tool `memory`. Non copiamo l'intero modello di sistema operativo. |
| [Sleep-time Compute, Lin et al., 2025](https://arxiv.org/html/2504.13171v1#S7) | Elaborare il contesto prima delle domande può aiutare, soprattutto quando le domande future sono prevedibili. I benefici dipendono dal compito e dal budget. | Un dream pianificato che prepara sintesi e nuclei. Limiti espliciti, recupero singolo e nessuna elaborazione speculativa continua. |
| [OpenClaw: memoria](https://docs.openclaw.ai/concepts/memory) e [SOUL.md](https://docs.openclaw.ai/reference/templates/SOUL) | File persistenti rendono l'identità e le informazioni conservate ispezionabili. Il soul può evolvere nel tempo. | `soul.md` breve, leggibile e versionato; identità personale distinta dalle regole di esecuzione. Prompt originale di Alina, non copiato dai template. |

## Ciò che entra nel POC

Un singolo ciclo agente, uno scheduler interno, SQLite e due viste Markdown.
Dream consolida i giorni conclusi, prepara nuclei con riferimenti alle fonti,
archivia quelli oltre i sette giorni e propone una revisione completa del soul.
Il runtime valida formato, limiti e provenienza delle citazioni prima di salvare.
Questi controlli non provano la verità di una sintesi: servono confronti con gli
originali e test su conversazioni reali.

L'introspezione può lasciare il soul invariato. Senza nuova attività non serve
riscrivere ogni notte la propria personalità. Nel POC il soul contiene al massimo
180 parole / 1600 byte e non cambia strumenti, permessi o configurazione.

Per la ricerca: embedding salvati in SQLite e similarità coseno calcolata in Go,
con un contributo testuale. È una scansione esatta, sufficiente per il primo
archivio personale; non richiede un'estensione nativa sul telefono. Quando il
numero di ricordi lo giustificherà, potremo aggiungere un indice come
[sqlite-vec](https://alexgarcia.xyz/sqlite-vec/go.html), mantenendo il formato dei
nuclei. Gli embedding richiedono un modello: senza un endpoint configurato il
POC dichiara esplicitamente la ricerca testuale.

## Cose da misurare prima di aggiungere complessità

- Ricorda correttamente una preferenza dopo due settimane e un riavvio?
- Distingue una proposta da un lavoro realmente completato?
- Una correzione dell'utente prevale su una vecchia convinzione?
- Quanto costano un giorno di memoria e il suo dream, in token, tempo e spazio?
- Il soul resta riconoscibile e corto dopo molte revisioni?

Le correzioni vengono oggi conservate come nuovi fatti datati. Fusione semantica
dei duplicati, relazioni esplicite di supersessione e cancellazione completa di
un fatto attraverso tutti gli archivi sono sviluppi successivi, da guidare con
questi test. Per ora evitiamo knowledge graph, agenti riflessivi multipli, punteggi
di importanza assegnati con chiamate aggiuntive e simulazioni di emozioni numeriche.
